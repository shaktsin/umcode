package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
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

// Removing a transaction predicate must leave these stale inputs unable to
// create either a durable operation or a provisional memory row.
func TestMemoryPrepareRechecksCanonicalPredicates(t *testing.T) {
	for _, mutation := range []string{"work", "work revision", "candidate", "candidate revision", "expired", "evidence membership", "evidence stale", "vault unavailable", "final revision", "source", "verification", "semantic key", "replacement", "validation absent"} {
		t.Run(mutation, func(t *testing.T) {
			st, in := memoryPromotionFixture(t)
			switch mutation {
			case "work":
				mustMemoryExec(t, st.DB, `UPDATE works SET status='open' WHERE id=?`, in.Memory.WorkID)
			case "work revision":
				mustMemoryExec(t, st.DB, `UPDATE works SET revision=revision+1 WHERE id=?`, in.Memory.WorkID)
			case "candidate":
				mustMemoryExec(t, st.DB, `UPDATE work_nodes SET status='rejected' WHERE id=?`, in.Memory.CandidateNodeID)
			case "candidate revision":
				mustMemoryExec(t, st.DB, `UPDATE work_nodes SET revision=revision+1 WHERE id=?`, in.Memory.CandidateNodeID)
			case "expired":
				mustMemoryExec(t, st.DB, `UPDATE work_nodes SET valid_until=? WHERE id=?`, Now(), in.Memory.CandidateNodeID)
			case "evidence membership":
				mustMemoryExec(t, st.DB, `DELETE FROM work_node_evidence WHERE node_id=?`, in.Memory.CandidateNodeID)
			case "evidence stale":
				mustMemoryExec(t, st.DB, `UPDATE evidence SET stale_at=? WHERE work_id=?`, Now(), in.Memory.WorkID)
			case "vault unavailable":
				mustMemoryExec(t, st.DB, `UPDATE evidence SET vault_hash='absent' WHERE work_id=?`, in.Memory.WorkID)
			case "final revision":
				mustMemoryExec(t, st.DB, `UPDATE work_fingerprints SET value='new' WHERE work_id=?`, in.Memory.WorkID)
			case "source":
				mustMemoryExec(t, st.DB, `UPDATE work_nodes SET status='superseded' WHERE kind='fact' AND work_id=?`, in.Memory.WorkID)
			case "verification":
				mustMemoryExec(t, st.DB, `UPDATE work_nodes SET status='stale' WHERE kind='criterion' AND work_id=?`, in.Memory.WorkID)
			case "semantic key":
				row := in.Memory
				row.ID = "other"
				row.Status = protocol.MemoryStatusActive
				insertTestMemory(t, st, row)
			case "replacement":
				in.ReplacesMemory = "missing"
			case "validation absent":
				in.Validate = nil
			}
			_, err := st.PrepareMemoryPromotion(t.Context(), in)
			if !errors.Is(err, ErrMemoryStale) && !errors.Is(err, ErrMemoryConflict) {
				t.Fatalf("err=%v", err)
			}
			for _, table := range []string{"memory_promotion_ops", "project_memories"} {
				var count int
				if err := st.DB.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE candidate_node_id=? AND id<> 'other'`, in.Memory.CandidateNodeID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("%s count=%d err=%v", table, count, err)
				}
			}
		})
	}
}

func TestMemoryPrepareAtomicRollback(t *testing.T) {
	st, in := memoryPromotionFixture(t)
	mustMemoryExec(t, st.DB, `CREATE TRIGGER no_promotion BEFORE INSERT ON memory_promotion_ops BEGIN SELECT RAISE(ABORT,'injected'); END`)
	if _, err := st.PrepareMemoryPromotion(t.Context(), in); err == nil {
		t.Fatal("expected injected error")
	}
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM project_memories`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial memory=%d err=%v", n, err)
	}
}

func TestMemoryConcurrencyReservesSemanticKey(t *testing.T) {
	st, in := memoryPromotionFixture(t)
	second := in
	second.Memory.ID = "second"
	// Different candidates compete for the same semantic identity.
	d, _ := st.GetWorkDetail(t.Context(), in.Memory.WorkID)
	candidate := d.Nodes[1]
	candidate.ID = "candidate-second"
	if _, err := st.AddWorkNode(t.Context(), candidate); err != nil {
		t.Fatal(err)
	}
	mustMemoryExec(t, st.DB, `INSERT INTO work_node_evidence(work_id,node_id,evidence_id) SELECT work_id,?,evidence_id FROM work_node_evidence WHERE node_id=?`, candidate.ID, in.Memory.CandidateNodeID)
	second.Memory.CandidateNodeID = candidate.ID
	in.Validate = func(MemoryPromotionSnapshot) error { return nil }
	second.Validate = in.Validate
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, input := range []PrepareMemoryPromotion{in, second} {
		wg.Add(1)
		go func(input PrepareMemoryPromotion) {
			defer wg.Done()
			_, err := st.PrepareMemoryPromotion(context.Background(), input)
			errs <- err
		}(input)
	}
	wg.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrMemoryConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflict=%d", successes, conflicts)
	}
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM memory_promotion_ops WHERE state='prepared'`).Scan(&n)
	if n != 1 {
		t.Fatalf("prepared=%d", n)
	}
}

func TestMemoryFinalizeReplacementAtomicAndIdempotent(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			st, in := memoryPromotionFixture(t)
			old := in.Memory
			old.ID = "old"
			old.Status = protocol.MemoryStatusActive
			old.Text = "- Old test command."
			old.TextHash = testMemoryHash([]byte(old.Text))
			old.CandidateNodeID = "original-candidate"
			insertTestMemory(t, st, old)
			in.ReplacesMemory = old.ID
			mustMemoryExec(t, st.DB, `UPDATE work_nodes SET content_json=json_set(content_json,'$.replaces_memory',?) WHERE id=?`, old.ID, in.Memory.CandidateNodeID)
			in.Validate = func(MemoryPromotionSnapshot) error { return nil }
			op, err := st.PrepareMemoryPromotion(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.CommitMemoryPromotion(t.Context(), op.ID, time.Now()); !errors.Is(err, ErrMemoryConflict) {
				t.Fatalf("unwritten commit=%v", err)
			}
			if err := st.MarkMemoryFileWritten(t.Context(), op.ID); err != nil {
				t.Fatal(err)
			}
			if fail {
				mustMemoryExec(t, st.DB, `CREATE TRIGGER no_commit BEFORE UPDATE OF state ON memory_promotion_ops WHEN NEW.state='committed' BEGIN SELECT RAISE(ABORT,'injected'); END`)
			}
			at := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
			err = st.CommitMemoryPromotion(t.Context(), op.ID, at)
			if fail {
				if err == nil {
					t.Fatal("expected rollback")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			newRow, _ := st.GetProjectMemory(t.Context(), op.MemoryID)
			oldRow, _ := st.GetProjectMemory(t.Context(), old.ID)
			detail, _ := st.GetWorkDetail(t.Context(), in.Memory.WorkID)
			if fail {
				if newRow.Status != "pending_repair" || oldRow.Status != "active" || detail.Nodes[1].Status != "pending" {
					t.Fatalf("partial finalization new=%+v old=%+v candidate=%+v", newRow, oldRow, detail.Nodes[1])
				}
				return
			}
			if newRow.Status != "active" || oldRow.Status != "superseded" || oldRow.SupersededBy != newRow.ID || detail.Nodes[1].Status != "promoted" || detail.Work.Status != "completed" {
				t.Fatalf("bad commit new=%+v old=%+v detail=%+v", newRow, oldRow, detail)
			}
			if newRow.WorkID != in.Memory.WorkID || newRow.EvidenceJSON != in.Memory.EvidenceJSON || oldRow.WorkID != old.WorkID || oldRow.CandidateNodeID != old.CandidateNodeID {
				t.Fatal("provenance changed")
			}
			if err := st.CommitMemoryPromotion(t.Context(), op.ID, at.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			again, _ := st.GetProjectMemory(t.Context(), op.MemoryID)
			after, _ := st.GetWorkDetail(t.Context(), in.Memory.WorkID)
			if !reflect.DeepEqual(again, newRow) || !reflect.DeepEqual(after, detail) {
				t.Fatal("repeated commit changed state")
			}
			if err := st.FailMemoryPromotion(t.Context(), op.ID, "conflicted", "conflicted", "cas"); !errors.Is(err, ErrMemoryConflict) {
				t.Fatalf("committed operation regressed: %v", err)
			}
		})
	}
}

func TestMemoryPromotionFailureStatesAndRecovery(t *testing.T) {
	for _, state := range []string{"conflicted", "pending_repair"} {
		t.Run(state, func(t *testing.T) {
			st, in := memoryPromotionFixture(t)
			op, err := st.PrepareMemoryPromotion(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			candidateStatus := "pending"
			if state == "conflicted" {
				candidateStatus = "conflicted"
			}
			if err := st.FailMemoryPromotion(t.Context(), op.ID, state, candidateStatus, "cas"); err != nil {
				t.Fatal(err)
			}
			row, _ := st.GetProjectMemory(t.Context(), op.MemoryID)
			d, _ := st.GetWorkDetail(t.Context(), in.Memory.WorkID)
			stored, err := scanMemoryPromotionOp(st.DB.QueryRow(`SELECT `+memoryPromotionOpCols+` FROM memory_promotion_ops WHERE id=?`, op.ID))
			if err != nil || row.Status != state || d.Nodes[1].Status != candidateStatus || stored.State != state || stored.ErrorClass != "cas" || d.Work.Status != "completed" {
				t.Fatalf("inconsistent failure row=%+v op=%+v detail=%+v err=%v", row, stored, d, err)
			}
			if state == "pending_repair" {
				if _, err := st.PrepareMemoryPromotion(t.Context(), in); !errors.Is(err, ErrMemoryConflict) {
					t.Fatalf("reservation released too early: %v", err)
				}
				if err := st.MarkMemoryFileWritten(t.Context(), op.ID); err != nil {
					t.Fatal(err)
				}
				if err := st.CommitMemoryPromotion(t.Context(), op.ID, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestMemoryPromotionExactDuplicatePreservesProvenance(t *testing.T) {
	st, in := memoryPromotionFixture(t)
	old := in.Memory
	old.ID = "old"
	old.Status = "active"
	old.WorkID = "original-work"
	old.CandidateNodeID = "original-candidate"
	insertTestMemory(t, st, old)
	in.BeforeBytes = in.AfterBytes
	in.Memory.FileHashBefore = in.Memory.FileHashAfter
	in.Validate = func(MemoryPromotionSnapshot) error { return nil }
	op, err := st.PrepareMemoryPromotion(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if op.MemoryID != old.ID || string(op.BeforeBytes) != string(op.AfterBytes) {
		t.Fatalf("duplicate op=%+v", op)
	}
	if err := st.CommitMemoryPromotion(t.Context(), op.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	after, _ := st.GetProjectMemory(t.Context(), old.ID)
	if !reflect.DeepEqual(after, old) {
		t.Fatalf("duplicate changed existing provenance: %+v", after)
	}
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM project_memories`).Scan(&n)
	if n != 1 {
		t.Fatalf("duplicate rows=%d", n)
	}
}

func testMemoryHash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

func memoryPromotionFixture(t *testing.T) (*Store, PrepareMemoryPromotion) {
	t.Helper()
	st, th := workFixture(t)
	ctx := t.Context()
	p, err := st.CreateProject(ctx, protocol.Project{Root: t.TempDir(), Name: "memory"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.CreateWork(ctx, protocol.Work{ThreadID: th.ID, ProjectID: p.ID, WorkflowDepth: "designed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddWorkNode(ctx, protocol.WorkNode{ID: "fact", WorkID: w.ID, Kind: "fact", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	content := json.RawMessage(`{"category":"command","semantic_key":"test.command","text":"- Run go test ./... for this project.","source_revision":"final"}`)
	candidate, err := st.AddWorkNode(ctx, protocol.WorkNode{WorkID: w.ID, Kind: "memory_candidate", Status: "pending", Content: content})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddWorkNode(ctx, protocol.WorkNode{WorkID: w.ID, Kind: "criterion", Status: "passed"}); err != nil {
		t.Fatal(err)
	}
	ev, err := st.AddEvidence(ctx, protocol.Evidence{WorkID: w.ID, NodeID: candidate.ID, Kind: "discovery", SourceRevision: "final"})
	if err != nil {
		t.Fatal(err)
	}
	mustMemoryExec(t, st.DB, `INSERT INTO work_node_evidence(work_id,node_id,evidence_id) VALUES(?,?,?)`, w.ID, candidate.ID, ev.ID)
	mustMemoryExec(t, st.DB, `INSERT INTO work_edges(work_id,from_node_id,relation,to_node_id) VALUES(?,?,?,?)`, w.ID, candidate.ID, "candidate_for", "fact")
	mustMemoryExec(t, st.DB, `INSERT INTO work_fingerprints(id,work_id,turn_id,kind,value,paths_json,taken_at) VALUES('final',?,'turn','turn_end','final','[]',?)`, w.ID, Now())
	if err := st.CloseWork(ctx, w.ID, "completed", time.Now()); err != nil {
		t.Fatal(err)
	}
	canonical, err := st.GetWorkDetail(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, after := []byte("# Instructions\n"), []byte("# Instructions\n- Run go test ./... for this project.\n")
	in := PrepareMemoryPromotion{Memory: protocol.ProjectMemory{ID: "new", ProjectID: p.ID, WorkID: w.ID, CandidateNodeID: candidate.ID, SemanticKey: "test.command", Category: "command", TargetPath: "UMCODE.md", Text: "- Run go test ./... for this project.", TextHash: testMemoryHash([]byte("- Run go test ./... for this project.")), SourceRevision: "final", EvidenceJSON: fmt.Sprintf("[%q]", ev.ID), FileHashBefore: testMemoryHash(before), FileHashAfter: testMemoryHash(after)}, ExpectedWorkRevision: canonical.Work.Revision, ExpectedCandidateRevision: candidate.Revision, ThreadID: th.ID, TurnID: "turn", BeforeBytes: before, AfterBytes: after}
	in.Validate = func(snapshot MemoryPromotionSnapshot) error {
		if !reflect.DeepEqual(snapshot.Detail, canonical) || snapshot.ProjectRoot != p.Root || snapshot.FinalRevision != "final" {
			return ErrMemoryStale
		}
		return nil
	}
	return st, in
}

func mustMemoryExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

// Candidate decoding canonicalizes a plain or alternate Markdown bullet before
// qualification. Preparation must compare that same canonical text.
func TestMemoryPrepareAcceptsCanonicalBulletNormalization(t *testing.T) {
	for _, text := range []string{"Run go test ./... for this project.", "  * Run go test ./... for this project.  ", "+ Run go test ./... for this project."} {
		t.Run(text, func(t *testing.T) {
			st, in := memoryPromotionFixture(t)
			mustMemoryExec(t, st.DB, `UPDATE work_nodes SET content_json=json_set(content_json,'$.text',?) WHERE id=?`, text, in.Memory.CandidateNodeID)
			in.Validate = func(MemoryPromotionSnapshot) error { return nil }
			if _, err := st.PrepareMemoryPromotion(t.Context(), in); err != nil {
				t.Fatalf("canonical bullet rejected: %v", err)
			}
		})
	}
}

func TestMemoryPromotionFailureAtomicRollback(t *testing.T) {
	st, in := memoryPromotionFixture(t)
	op, err := st.PrepareMemoryPromotion(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	mustMemoryExec(t, st.DB, `CREATE TRIGGER no_failure BEFORE UPDATE OF state ON memory_promotion_ops BEGIN SELECT RAISE(ABORT,'injected'); END`)
	if err := st.FailMemoryPromotion(t.Context(), op.ID, "conflicted", "conflicted", "cas"); err == nil {
		t.Fatal("expected injected failure")
	}
	row, _ := st.GetProjectMemory(t.Context(), op.MemoryID)
	d, _ := st.GetWorkDetail(t.Context(), in.Memory.WorkID)
	if row.Status != "pending_repair" || d.Nodes[1].Status != "pending" {
		t.Fatalf("partial failure transition: memory=%+v candidate=%+v", row, d.Nodes[1])
	}
}
