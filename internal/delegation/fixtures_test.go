package delegation

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/worktree"
)

func workspaceGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	b, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}
func workspaceWrite(t *testing.T, root, path, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, path), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func workspaceFixture(t *testing.T) (*WorkspaceService, *store.Store, protocol.AgentTask, protocol.AgentChild) {
	t.Helper()
	root := t.TempDir()
	workspaceGit(t, root, "init", "-q")
	workspaceGit(t, root, "config", "user.name", "Test")
	workspaceGit(t, root, "config", "user.email", "test@invalid")
	workspaceWrite(t, root, "tracked", "base\n")
	workspaceWrite(t, root, "removed", "remove\n")
	workspaceGit(t, root, "add", ".")
	workspaceGit(t, root, "commit", "-qm", "base")
	s, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	th, err := s.CreateThread(t.Context(), protocol.Thread{Title: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	tr := protocol.Turn{ID: "root", ThreadID: th.ID, Status: protocol.TurnCompleted, StartedAt: time.Now()}
	if err = s.CreateTurn(t.Context(), tr); err != nil {
		t.Fatal(err)
	}
	task := protocol.AgentTask{ID: "task", ThreadID: th.ID, RootTurnID: tr.ID, Activation: protocol.AgentActivationExplicitUser, State: protocol.AgentStateRunning, BaseRoot: root, ParentRoot: root, BaseRevision: workspaceGit(t, root, "rev-parse", "HEAD")}
	if err = s.CreateAgentTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateAgentChild(t.Context(), protocol.AgentChild{ID: "implementer", TaskID: task.ID, Role: protocol.AgentRoleImplementer, Assignment: protocol.AgentAssignment{Prompt: "edit"}}, "call")
	if err != nil {
		t.Fatal(err)
	}
	svc := NewWorkspaceService(s, worktree.New(t.TempDir()), nil, nil)
	return svc, s, task, child
}
