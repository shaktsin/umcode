package store

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestTaskWorkspacePersistsOwnership(t *testing.T) {
	s, task := runtimeFixture(t)
	record := AgentTaskWorkspace{TaskID: task.ID, SourceRoot: "/project", ExecutionRoot: "/private/task", SourceHead: "head", SnapshotCommit: "snapshot", Manifest: json.RawMessage(`{"paths":{}}`), State: "active"}
	if err := s.SaveAgentTaskWorkspace(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	got, err := s.AgentTaskWorkspace(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskID != record.TaskID || got.SourceRoot != record.SourceRoot || got.ExecutionRoot != record.ExecutionRoot || got.SnapshotCommit != record.SnapshotCommit || string(got.Manifest) != string(record.Manifest) || got.State != "active" {
		t.Fatalf("got %+v", got)
	}
	if err := s.SaveAgentTaskWorkspace(t.Context(), record); err != nil {
		t.Fatalf("idempotent save: %v", err)
	}
	changed := record
	changed.ExecutionRoot = "/private/other"
	if err := s.SaveAgentTaskWorkspace(t.Context(), changed); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("changed ownership: %v", err)
	}
	if _, err := s.AgentTaskWorkspace(t.Context(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing workspace: %v", err)
	}
}
