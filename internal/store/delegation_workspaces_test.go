package store

import (
	"github.com/shaktsin/umcode/internal/protocol"
	"path/filepath"
	"testing"
)

func TestAgentInspectionMigrationAndOwnedUniqueness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	task := agentTaskFixture(t, s, "inspections")
	child, err := s.CreateAgentChild(t.Context(), protocol.AgentChild{TaskID: task.ID, Role: protocol.AgentRoleImplementer, Assignment: protocol.AgentAssignment{Prompt: "edit"}}, "implementer")
	if err != nil {
		t.Fatal(err)
	}
	old := AgentWorkspace{ID: "legacy-inspection", TaskID: task.ID, ChildID: child.ID, Kind: "reviewer", Root: "/owned/inspection", ParentRoot: "/owned/implementer", BaseRevision: "result1", ResultRevision: "result1", SourceID: child.ID}
	if err = s.SaveAgentWorkspace(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`DROP INDEX idx_agent_owned_workspaces; ALTER TABLE agent_workspaces RENAME TO before_upgrade; CREATE TABLE agent_workspaces(id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,child_id TEXT NOT NULL REFERENCES agent_children(id) ON DELETE CASCADE,kind TEXT NOT NULL CHECK(kind IN ('implementer','reviewer')),data_json TEXT NOT NULL,UNIQUE(child_id,kind)); INSERT INTO agent_workspaces SELECT * FROM before_upgrade; DROP TABLE before_upgrade; DELETE FROM schema_migrations WHERE version=28;`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	migrated, err := s.AgentWorkspace(t.Context(), old.ID)
	if err != nil || migrated.Kind != "inspection" || migrated.Root != old.Root || migrated.ResultRevision != old.ResultRevision {
		t.Fatalf("legacy inspection lost %+v %v", migrated, err)
	}
	next := migrated
	next.ID = "inspection-result2"
	next.BaseRevision = "result2"
	next.ResultRevision = "result2"
	if err = s.SaveAgentWorkspace(t.Context(), next); err != nil {
		t.Fatalf("revision-specific inspection rejected %v", err)
	}
	for _, kind := range []string{"implementer", "reviewer"} {
		t.Run(kind, func(t *testing.T) {
			owner := child
			if kind == "reviewer" {
				var err error
				owner, err = s.CreateAgentChild(t.Context(), protocol.AgentChild{TaskID: task.ID, Role: protocol.AgentRoleReviewer, Assignment: protocol.AgentAssignment{Prompt: "review"}}, "bound-reviewer")
				if err != nil {
					t.Fatal(err)
				}
			}
			w := AgentWorkspace{ID: kind + "-owned", TaskID: task.ID, ChildID: owner.ID, Kind: kind, Root: "/owned/" + kind, BaseRevision: "base"}
			if err := s.SaveAgentWorkspace(t.Context(), w); err != nil {
				t.Fatal(err)
			}
			w.ID += "-duplicate"
			if err := s.SaveAgentWorkspace(t.Context(), w); err == nil {
				t.Fatal("owned child/kind uniqueness relaxed")
			}
		})
	}
}
