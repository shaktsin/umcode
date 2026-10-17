package delegation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestScopedContext(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "code.go"), []byte("relevant code"), 0600); err != nil {
		t.Fatal(err)
	}
	b := ScopedContextBuilder{Root: root, Summary: "bounded summary", Guidance: "project guidance"}
	child := protocol.AgentChild{Assignment: protocol.AgentAssignment{Prompt: "implement feature", AcceptanceChecks: []string{"acceptance marker"}, ContextRefs: []string{"code.go"}}}
	msgs, err := b.Build(context.Background(), protocol.AgentTask{}, child)
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, m := range msgs {
		for _, p := range m.Parts {
			text += p.Text
		}
	}
	for _, want := range []string{"implement feature", "acceptance marker", "relevant code", "bounded summary", "project guidance"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q", want)
		}
	}
	for _, tt := range []struct {
		name   string
		mutate func(*ScopedContextBuilder, *protocol.AgentChild)
	}{
		{"assignment", func(b *ScopedContextBuilder, c *protocol.AgentChild) { c.Assignment.Prompt = strings.Repeat("x", 8193) }},
		{"criteria", func(b *ScopedContextBuilder, c *protocol.AgentChild) {
			c.Assignment.AcceptanceChecks = []string{strings.Repeat("x", 8193)}
		}},
		{"summary", func(b *ScopedContextBuilder, c *protocol.AgentChild) { b.Summary = strings.Repeat("x", 16385) }},
		{"reference", func(b *ScopedContextBuilder, c *protocol.AgentChild) {
			os.WriteFile(filepath.Join(root, "big"), []byte(strings.Repeat("x", 32769)), 0600)
			c.Assignment.ContextRefs = []string{"big"}
		}},
		{"credential", func(b *ScopedContextBuilder, c *protocol.AgentChild) {
			os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0600)
			c.Assignment.ContextRefs = []string{".env"}
		}},
		{"escape", func(b *ScopedContextBuilder, c *protocol.AgentChild) {
			c.Assignment.ContextRefs = []string{"../secret"}
		}},
		{"symlink", func(b *ScopedContextBuilder, c *protocol.AgentChild) {
			os.Symlink(t.TempDir(), filepath.Join(root, "escape"))
			c.Assignment.ContextRefs = []string{"escape/file"}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			copyB, copyC := b, child
			tt.mutate(&copyB, &copyC)
			if _, err := copyB.Build(t.Context(), protocol.AgentTask{}, copyC); err == nil {
				t.Fatal("unsafe or oversized context accepted")
			}
		})
	}
}

// Opening a FIFO before validating its type blocks without honoring ctx.
// The failure branch connects a writer to release the old implementation.
func TestScopedContextRejectsFIFO(t *testing.T) {
	program, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("FIFO fixture unavailable on this platform")
	}
	root := t.TempDir()
	path := filepath.Join(root, "reference")
	if out, err := exec.Command(program, path).CombinedOutput(); err != nil {
		t.Fatalf("create FIFO: %v %s", err, out)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (ScopedContextBuilder{Root: root}).Build(ctx, protocol.AgentTask{}, protocol.AgentChild{Assignment: protocol.AgentAssignment{Prompt: "inspect", ContextRefs: []string{"reference"}}})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("FIFO not rejected as nonregular: %v", err)
		}
	case <-time.After(time.Second):
		cancel()
		writer, err := os.OpenFile(path, os.O_WRONLY, 0600)
		if err == nil {
			writer.Close()
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("FIFO reader stayed blocked after cleanup writer")
		}
		t.Fatal("special reference blocked before file-type validation")
	}
}
