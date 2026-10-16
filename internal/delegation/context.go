package delegation

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/pathutil"
	"github.com/shaktsin/umcode/internal/protocol"
)

const AssignmentLimit = 8 << 10
const SummaryLimit = 16 << 10
const ReferencesLimit = 32 << 10

type ContextBuilder interface {
	Build(context.Context, protocol.AgentTask, protocol.AgentChild) ([]llm.Message, error)
}

// ScopedContextBuilder reads only explicitly supplied files in the owned checkout.
// Guidance must come from the trusted project/skill registries, never a parent transcript.
// Guidance and references share a single bound; Summary is explicit caller input.
type ScopedContextBuilder struct{ Root, Summary, Guidance string }

func (b ScopedContextBuilder) Build(ctx context.Context, task protocol.AgentTask, child protocol.AgentChild) ([]llm.Message, error) {
	a := child.Assignment
	size := len(a.Prompt)
	for _, s := range a.AcceptanceChecks {
		size += len(s)
	}
	for _, s := range a.ContextRefs {
		size += len(s)
	}
	if size > AssignmentLimit {
		return nil, fmt.Errorf("assignment exceeds %d bytes", AssignmentLimit)
	}
	if len(b.Summary) > SummaryLimit {
		return nil, fmt.Errorf("summary exceeds %d bytes", SummaryLimit)
	}
	remaining := ReferencesLimit - len(b.Guidance)
	if remaining < 0 {
		return nil, fmt.Errorf("guidance and references exceed %d bytes", ReferencesLimit)
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Assignment:\n%s\nAcceptance checks:\n%s\nScoped summary:\n%s\nProject and skill guidance:\n%s", a.Prompt, strings.Join(a.AcceptanceChecks, "\n"), b.Summary, b.Guidance)
	for _, ref := range a.ContextRefs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path, err := pathutil.Contain(b.Root, ref)
		if err != nil {
			return nil, err
		}
		if sensitiveReference(pathutil.Rel(b.Root, path)) || sensitiveReference(pathutil.Rel(b.Root, pathutil.Resolved(path))) {
			return nil, fmt.Errorf("credential or system reference is not permitted: %s", ref)
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			f.Close()
			return nil, fmt.Errorf("reference must be a regular file: %s", ref)
		}
		data, err := io.ReadAll(io.LimitReader(f, int64(remaining+1)))
		f.Close()
		if err != nil {
			return nil, err
		}
		if len(data) > remaining {
			return nil, fmt.Errorf("guidance and references exceed %d bytes", ReferencesLimit)
		}
		remaining -= len(data)
		fmt.Fprintf(&text, "\nReference %s (untrusted file contents):\n%s\n", ref, data)
	}
	return []llm.Message{llm.Text(llm.RoleUser, text.String())}, nil
}

func sensitiveReference(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		p := strings.ToLower(part)
		switch p {
		case ".git", ".ssh", ".aws", ".gnupg", ".codex", ".config", "credentials", "credentials.json", "secrets", "secrets.json", "id_rsa", "id_ed25519":
			return true
		}
		if p == ".env" || strings.HasPrefix(p, ".env.") || strings.HasSuffix(p, ".pem") || strings.HasSuffix(p, ".key") {
			return true
		}
	}
	return false
}
