package delegation

import (
	"testing"

	"github.com/shaktsin/umcode/internal/store"
)

func TestHistoricalAgentSnapshotRemainsScopedAndReadOnly(t *testing.T) {
	svc, s, task, _ := workspaceFixture(t)
	const childID = "earlier-agent"
	const wsID = "earlier-workspace"
	_, err := s.DB.ExecContext(t.Context(), `INSERT INTO agent_children
 (id,task_id,call_id,role,assignment_json,state,revision,workspace_id,result_json,usage_json,version,created_at,updated_at,model_json)
 VALUES (?,?,?,'implementer','{"prompt":"earlier work"}','completed','','','{}','{}',1,?,?, '{}')`,
		childID, task.ID, "legacy:earlier-agent", store.Now(), store.Now())
	if err != nil {
		t.Fatal(err)
	}
	ws, err := svc.Manager.EnsureCommitted(t.Context(), wsID, task.ParentRoot, task.BaseRevision)
	if err != nil {
		t.Fatal(err)
	}
	workspaceWrite(t, ws.Path, "tracked", "historical result\n")
	workspaceGit(t, ws.Path, "add", "tracked")
	workspaceGit(t, ws.Path, "commit", "-qm", "historical result")
	revision := workspaceGit(t, ws.Path, "rev-parse", "HEAD")
	if _, err := s.DB.ExecContext(t.Context(), `UPDATE agent_children SET revision=?,workspace_id=? WHERE id=?`, revision, wsID, childID); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAgentWorkspace(t.Context(), store.AgentWorkspace{ID: wsID, TaskID: task.ID, ChildID: childID, Kind: "implementer", Root: ws.Path, ParentRoot: task.ParentRoot, BaseRevision: task.BaseRevision, ResultRevision: revision}); err != nil {
		t.Fatal(err)
	}
	workspaceWrite(t, ws.Path, "tracked", "later mutable checkout\n")
	got, err := svc.ReadSnapshotFile(t.Context(), task.ID, childID, revision, "tracked")
	if err != nil || got.Content != "historical result\n" {
		t.Fatalf("historical snapshot = %+v, %v", got, err)
	}
	for _, path := range []string{"../tracked", "/etc/passwd", ".git/config", ".aws/credentials"} {
		if _, err := svc.ReadSnapshotFile(t.Context(), task.ID, childID, revision, path); err == nil {
			t.Fatalf("unsafe historical path %q accepted", path)
		}
	}
	if _, err := svc.ReadSnapshotFile(t.Context(), task.ID, childID, task.BaseRevision, "tracked"); err == nil {
		t.Fatal("a different revision read the historical file")
	}
}
