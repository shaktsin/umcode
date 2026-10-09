package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

// Catches lost fields, wrong filters, duplicate active keys, and cascading
// deletion of durable provenance when its source chat is removed.
func TestCuratedMemoryMigrationAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, th := workFixture(t)
	p, err := st.CreateProject(ctx, protocol.Project{Root: t.TempDir(), Name: "memory"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateProject(ctx, protocol.Project{Root: t.TempDir(), Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.CreateWork(ctx, protocol.Work{ThreadID: th.ID, ProjectID: p.ID, WorkflowDepth: "designed"})
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.AddWorkNode(ctx, protocol.WorkNode{WorkID: w.ID, Kind: "memory_candidate", Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := st.AddEvidence(ctx, protocol.Evidence{WorkID: w.ID, NodeID: n.ID, Kind: "discovery"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 8, 12, 0, 0, 123, time.UTC)
	m := protocol.ProjectMemory{
		ID: "mem_active", ProjectID: p.ID, WorkID: w.ID, CandidateNodeID: n.ID,
		SemanticKey: "test.command", Category: "command", TargetPath: "UMCODE.md", Text: "Run go test ./...", TextHash: "text-hash",
		Status: "active", SourceRevision: "revision", EvidenceJSON: fmt.Sprintf("[%q]", ev.ID),
		FileHashBefore: "before-hash", FileHashAfter: "after-hash", SupersededBy: "", CreatedAt: at, PromotedAt: &at,
	}
	insertTestMemory(t, st, m)
	got, err := st.GetProjectMemory(ctx, m.ID)
	if err != nil || !reflect.DeepEqual(got, m) {
		t.Fatalf("memory=%+v want=%+v err=%v", got, m, err)
	}
	if _, err := st.GetProjectMemory(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing memory err=%v", err)
	}
	duplicate := m
	duplicate.ID = "mem_duplicate"
	if err := execTestMemory(st, duplicate); err == nil {
		t.Fatal("duplicate active project semantic key accepted")
	}
	for _, status := range []string{"superseded", "conflicted", "pending_repair"} {
		history := m
		history.ID, history.Status, history.PromotedAt, history.SupersededBy = "mem_"+status, status, nil, m.ID
		insertTestMemory(t, st, history)
		got, err := st.GetProjectMemory(ctx, history.ID)
		if err != nil || !reflect.DeepEqual(got, history) {
			t.Fatalf("history=%+v err=%v", got, err)
		}
	}
	for _, variant := range []string{"other_project", "other_path"} {
		copy := m
		copy.ID = "mem_" + variant
		if variant == "other_project" {
			copy.ProjectID = other.ID
		} else {
			copy.TargetPath, copy.SemanticKey = "sub/UMCODE.md", "sub.command"
		}
		insertTestMemory(t, st, copy)
	}
	active, err := st.ListActiveProjectMemories(ctx, p.ID, "UMCODE.md")
	if err != nil || !reflect.DeepEqual(active, []protocol.ProjectMemory{m}) {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	active, err = st.ListActiveProjectMemories(ctx, p.ID, "missing/UMCODE.md")
	if err != nil || len(active) != 0 {
		t.Fatalf("missing path active=%+v err=%v", active, err)
	}
	var wantOps []protocol.MemoryPromotionOp
	for i, state := range []string{"prepared", "file_written", "pending_repair", "committed", "conflicted"} {
		op := protocol.MemoryPromotionOp{ID: "mpo_" + state, ProjectID: p.ID, WorkID: w.ID, CandidateNodeID: n.ID,
			MemoryID: m.ID, ThreadID: th.ID, TurnID: "trn_source", TargetPath: "UMCODE.md", State: state,
			FileHashBefore: "before-hash", FileHashAfter: "after-hash", BeforeBytes: []byte{0, 1, 255}, AfterBytes: []byte("after\n"),
			ErrorClass: "retryable", CreatedAt: at.Add(time.Duration(i) * time.Second), UpdatedAt: at.Add(time.Minute)}
		if state == "prepared" {
			op.BeforeBytes = nil // A new file has no previous bytes.
		} else if state == "file_written" {
			op.BeforeBytes = []byte{} // An existing file may be empty.
		}
		insertTestPromotionOp(t, st, op)
		if i < 3 {
			wantOps = append(wantOps, op)
		}
	}
	ops, err := st.ListIncompleteMemoryPromotionOps(ctx)
	if err != nil || !reflect.DeepEqual(ops, wantOps) {
		t.Fatalf("ops=%+v want=%+v err=%v", ops, wantOps, err)
	}
	if err := st.DeleteThread(ctx, th.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetWorkDetail(ctx, w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("source work still present: %v", err)
	}
	got, err = st.GetProjectMemory(ctx, m.ID)
	if err != nil || !reflect.DeepEqual(got, m) {
		t.Fatalf("memory after source deletion=%+v err=%v", got, err)
	}
	ops, err = st.ListIncompleteMemoryPromotionOps(ctx)
	if err != nil || !reflect.DeepEqual(ops, wantOps) {
		t.Fatalf("audit after source deletion=%+v err=%v", ops, err)
	}
	if err := st.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"project_memories", "memory_promotion_ops"} {
		var count int
		if err := st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE project_id = ?`, p.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s after project deletion count=%d err=%v", table, count, err)
		}
	}
	if _, err := st.GetProjectMemory(ctx, "mem_other_project"); err != nil {
		t.Fatalf("other project's memory lost: %v", err)
	}
	// The schema also protects direct project deletion via its project FK.
	if _, err := st.DB.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetProjectMemory(ctx, "mem_other_project"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("project cascade err=%v", err)
	}
}

func execTestMemory(st *Store, m protocol.ProjectMemory) error {
	var promoted any
	if m.PromotedAt != nil {
		promoted = FormatTime(*m.PromotedAt)
	}
	_, err := st.DB.Exec(`INSERT INTO project_memories
		(id, project_id, work_id, candidate_node_id, semantic_key, category, target_path, text, text_hash, status,
		source_revision, evidence_json, file_hash_before, file_hash_after, superseded_by, created_at, promoted_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.ProjectID, m.WorkID, m.CandidateNodeID, m.SemanticKey, m.Category, m.TargetPath, m.Text, m.TextHash,
		m.Status, m.SourceRevision, m.EvidenceJSON, m.FileHashBefore, m.FileHashAfter, m.SupersededBy, FormatTime(m.CreatedAt), promoted)
	return err
}

func insertTestMemory(t *testing.T, st *Store, m protocol.ProjectMemory) {
	t.Helper()
	if err := execTestMemory(st, m); err != nil {
		t.Fatal(err)
	}
}

func insertTestPromotionOp(t *testing.T, st *Store, op protocol.MemoryPromotionOp) {
	t.Helper()
	// This driver binds a typed nil []byte as a zero-length BLOB. Use an
	// untyped nil to exercise the nullable snapshot scan separately.
	var before, after any
	if op.BeforeBytes != nil {
		before = op.BeforeBytes
	}
	if op.AfterBytes != nil {
		after = op.AfterBytes
	}
	_, err := st.DB.Exec(`INSERT INTO memory_promotion_ops
		(id, project_id, work_id, candidate_node_id, memory_id, thread_id, turn_id, target_path, state,
		file_hash_before, file_hash_after, before_bytes, after_bytes, error_class, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, op.ID, op.ProjectID, op.WorkID, op.CandidateNodeID, op.MemoryID,
		op.ThreadID, op.TurnID, op.TargetPath, op.State, op.FileHashBefore, op.FileHashAfter, before, after,
		op.ErrorClass, FormatTime(op.CreatedAt), FormatTime(op.UpdatedAt))
	if err != nil {
		t.Fatal(err)
	}
}

func TestPromotionFileChangeIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st, th := workFixture(t)
	before := "previous\n"
	c := FileChange{ThreadID: th.ID, TurnID: "turn", Path: "UMCODE.md", Action: "edit", Before: &before,
		Additions: 1, Revertable: true, PromotionOpID: "mpo_once"}
	first, err := st.RecordFileChange(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	c.Before, c.Additions = nil, 99
	second, err := st.RecordFileChange(ctx, c)
	if err != nil || first != second {
		t.Fatalf("first=%d second=%d err=%v", first, second, err)
	}
	changes, err := st.ListFileChanges(ctx, "", "turn", "UMCODE.md", 0)
	if err != nil || len(changes) != 1 || changes[0].PromotionOpID != "mpo_once" || changes[0].Before == nil || *changes[0].Before != before || changes[0].Additions != 1 {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}
	c.PromotionOpID = ""
	third, err := st.RecordFileChange(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	fourth, err := st.RecordFileChange(ctx, c)
	if err != nil || third == fourth {
		t.Fatalf("ordinary changes reused IDs %d %d err=%v", third, fourth, err)
	}
	// Existing/raw callers may omit the nullable column entirely.
	if _, err := st.DB.Exec(`INSERT INTO file_changes (project_id, thread_id, turn_id, path, action, created_at) VALUES ('', ?, 'turn', 'UMCODE.md', 'edit', ?)`, th.ID, Now()); err != nil {
		t.Fatal(err)
	}
	changes, err = st.ListFileChanges(ctx, "", "turn", "UMCODE.md", 0)
	if err != nil || len(changes) != 4 || changes[0].PromotionOpID != "" {
		t.Fatalf("ordinary history=%+v err=%v", changes, err)
	}
}
