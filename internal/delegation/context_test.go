package delegation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
