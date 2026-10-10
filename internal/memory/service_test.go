package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/projects"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
)

func serviceFixture(t *testing.T) (*Service, *store.Store, Request) {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProject(t.Context(), protocol.Project{Root: root, Name: "memory"})
	if err != nil {
		t.Fatal(err)
	}
	req := addCompletedWork(t, st, p, "test-command", "Use go test ./... for this repository")
	s := New(st, projects.New(st, config.Default(t.TempDir())), 4096)
	return s, st, req
}

func addCompletedWork(t *testing.T, st *store.Store, p protocol.Project, key, text string) Request {
	t.Helper()
	ctx := t.Context()
	th, err := st.CreateThread(ctx, protocol.Thread{ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	w, err := st.CreateWork(ctx, protocol.Work{ThreadID: th.ID, ProjectID: p.ID, WorkflowDepth: protocol.DepthDesigned})
	if err != nil {
		t.Fatal(err)
	}
	in := qualifyFixture()
	changeContent(&in, "scope_paths", nil)
	changeContent(&in, "semantic_key", key)
	changeContent(&in, "text", text)
	ids := map[string]string{}
	for _, n := range in.Detail.Nodes {
		ids[n.ID] = store.NewID("node")
	}
	for _, e := range in.Detail.Evidence {
		ids[e.ID] = store.NewID("ev")
	}
	for _, n := range in.Detail.Nodes {
		n.ID, n.WorkID = ids[n.ID], w.ID
		n.EvidenceIDs = nil
		if _, err := st.AddWorkNode(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range in.Detail.Evidence {
		e.ID, e.WorkID, e.NodeID = ids[e.ID], w.ID, ids[e.NodeID]
		if _, err := st.AddEvidence(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range in.Candidate.EvidenceIDs {
		execService(t, st, `INSERT INTO work_node_evidence(work_id,node_id,evidence_id) VALUES(?,?,?)`, w.ID, ids["candidate"], ids[id])
	}
	for _, e := range in.Detail.Edges {
		e.WorkID, e.FromNodeID, e.ToNodeID = w.ID, ids[e.FromNodeID], ids[e.ToNodeID]
		if err := st.AddWorkEdge(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	fp := store.NewID("fp")
	execService(t, st, `INSERT INTO work_fingerprints(id,work_id,turn_id,kind,value,paths_json,taken_at) VALUES(?,?,?,'verification','final','[]',?)`, fp, w.ID, "turn", store.Now())
	a := in.Detail.Attempts[0]
	a.ID, a.WorkID, a.CriterionNodeID, a.EvidenceID, a.FingerprintID = "", w.ID, ids[a.CriterionNodeID], ids[a.EvidenceID], fp
	if _, err := st.AddVerificationAttempt(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseWork(ctx, w.ID, protocol.WorkCompleted, time.Now()); err != nil {
		t.Fatal(err)
	}
	return Request{Project: p, WorkID: w.ID, ThreadID: th.ID, TurnID: "turn"}
}

func execService(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.DB.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
func readTarget(t *testing.T, p protocol.Project) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(p.Root, "UMCODE.md"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func assertCompleted(t *testing.T, st *store.Store, req Request) {
	t.Helper()
	d, err := st.GetWorkDetail(t.Context(), req.WorkID)
	if err != nil || d.Work.Status != protocol.WorkCompleted {
		t.Fatalf("work changed: %+v %v", d.Work, err)
	}
}

func TestPromotionBoundaries(t *testing.T) {
	for _, stage := range []string{"prepare", "recheck", "create", "write", "chmod", "sync", "rename", "after_rename", "record", "mark_written", "finalize", "parent_sync"} {
		t.Run(stage, func(t *testing.T) {
			s, st, req := serviceFixture(t)
			original := "# Instructions\r\nUser content.\r\n"
			abs := filepath.Join(req.Project.Root, "UMCODE.md")
			if err := os.WriteFile(abs, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			s.boundary = func(at string) error {
				if at == stage {
					return errors.New("injected")
				}
				return nil
			}
			r := s.PromoteCompleted(t.Context(), req)
			if r.Pending != 1 || r.Promoted != 0 {
				t.Fatalf("report=%+v", r)
			}
			post := stage == "after_rename" || stage == "record" || stage == "mark_written" || stage == "finalize" || stage == "parent_sync"
			got := readTarget(t, req.Project)
			if !post && got != original {
				t.Fatalf("pre-rename bytes changed: %q", got)
			}
			if post && !strings.Contains(got, "Use go test") {
				t.Fatalf("missing renamed bytes: %q", got)
			}
			ops, err := st.ListIncompleteMemoryPromotionOps(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if stage == "prepare" {
				want = 0
			}
			if len(ops) != want {
				t.Fatalf("ops=%+v", ops)
			}
			if len(ops) > 0 && ops[0].State != protocol.MemoryOpPendingRepair {
				t.Fatalf("state=%s", ops[0].State)
			}
			if matches, _ := filepath.Glob(filepath.Join(req.Project.Root, ".umcode-memory-*")); len(matches) != 0 {
				t.Fatalf("temps leaked: %v", matches)
			}
			assertCompleted(t, st, req)
		})
	}
}

func TestPromotionSuccessDuplicateAndConcurrency(t *testing.T) {
	s, st, req := serviceFixture(t)
	r := s.PromoteCompleted(t.Context(), req)
	if r.Promoted != 1 || r.Inserted != 1 {
		t.Fatalf("report=%+v", r)
	}
	abs := filepath.Join(req.Project.Root, "UMCODE.md")
	info, err := os.Stat(abs)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("permissions=%v %v", info, err)
	}
	duplicate := addCompletedWork(t, st, req.Project, "test-command", "Use go test ./... for this repository")
	s.boundary = func(at string) error {
		if at == "create" {
			return errors.New("must not rewrite")
		}
		return nil
	}
	r = s.PromoteCompleted(t.Context(), duplicate)
	if r.Promoted != 1 || r.Unchanged != 1 {
		t.Fatalf("duplicate=%+v", r)
	}
	changes, err := st.ListFileChanges(t.Context(), req.Project.ID, "", "", 100)
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes=%+v %v", changes, err)
	}
	d, _ := st.GetWorkDetail(t.Context(), duplicate.WorkID)
	for _, n := range d.Nodes {
		if n.Kind == protocol.NodeMemoryCandidate && n.Status != protocol.StatusPromoted {
			t.Fatalf("candidate=%+v", n)
		}
	}
	s.boundary = nil
	a := addCompletedWork(t, st, req.Project, "build-command", "Use go build ./... for this repository")
	b := addCompletedWork(t, st, req.Project, "vet-command", "Use go vet ./... for this repository")
	var wg sync.WaitGroup
	results := make(chan Report, 2)
	for _, q := range []Request{a, b} {
		wg.Add(1)
		go func(q Request) { defer wg.Done(); results <- s.PromoteCompleted(context.Background(), q) }(q)
	}
	wg.Wait()
	close(results)
	for r := range results {
		if r.Promoted != 1 {
			t.Fatalf("concurrent=%+v", r)
		}
	}
	got := readTarget(t, req.Project)
	for _, text := range []string{"go test", "go build", "go vet"} {
		if strings.Count(got, text) != 1 {
			t.Fatalf("lost update: %q", got)
		}
	}
}

func TestPromotionExternalEditAndSymlinkSwap(t *testing.T) {
	for _, swap := range []string{"edit", "file_symlink", "parent_symlink", "regular_swap"} {
		t.Run(swap, func(t *testing.T) {
			s, st, req := serviceFixture(t)
			abs := filepath.Join(req.Project.Root, "UMCODE.md")
			os.WriteFile(abs, []byte("original\n"), 0640)
			outside := filepath.Join(t.TempDir(), "private")
			os.WriteFile(outside, []byte("foreign\n"), 0644)
			s.boundary = func(at string) error {
				if at != "rename" {
					return nil
				}
				switch swap {
				case "edit":
					return os.WriteFile(abs, []byte("external\n"), 0644)
				case "file_symlink":
					os.Remove(abs)
					return os.Symlink(outside, abs)
				case "regular_swap":
					os.Rename(abs, abs+".old")
					return os.WriteFile(abs, []byte("original\n"), 0644)
				case "parent_symlink":
					os.Rename(req.Project.Root, req.Project.Root+".old")
					return os.Symlink(filepath.Dir(outside), req.Project.Root)
				}
				return nil
			}
			r := s.PromoteCompleted(t.Context(), req)
			if r.Conflicted != 1 {
				t.Fatalf("swap=%s report=%+v", swap, r)
			}
			foreign, _ := os.ReadFile(outside)
			if string(foreign) != "foreign\n" {
				t.Fatal("foreign file modified")
			}
			if swap == "edit" && readTarget(t, req.Project) != "external\n" {
				t.Fatal("external edit overwritten")
			}
			assertCompleted(t, st, req)
		})
	}
}

func TestPromotionRejectsSwapsThroughoutTempPreparation(t *testing.T) {
	for _, stage := range []string{"create", "write", "chmod", "sync", "rename"} {
		t.Run(stage, func(t *testing.T) {
			s, _, req := serviceFixture(t)
			abs := filepath.Join(req.Project.Root, "UMCODE.md")
			os.WriteFile(abs, []byte("original\n"), 0644)
			outside := t.TempDir()
			swapped := false
			s.boundary = func(at string) error {
				if swapped {
					t.Fatalf("continued to %s after directory swap at %s", at, stage)
				}
				if at == stage {
					if err := os.Rename(req.Project.Root, req.Project.Root+".moved"); err != nil {
						return err
					}
					swapped = true
					return os.Symlink(outside, req.Project.Root)
				}
				return nil
			}
			if r := s.PromoteCompleted(t.Context(), req); r.Conflicted != 1 {
				t.Fatalf("report=%+v", r)
			}
			b, err := os.ReadFile(filepath.Join(req.Project.Root+".moved", "UMCODE.md"))
			if err != nil || string(b) != "original\n" {
				t.Fatalf("moved target changed: %q %v", b, err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("foreign writes: %v %v", entries, err)
			}
		})
	}
}

func TestPromotionScopedReplacementPreservesPermissions(t *testing.T) {
	s, st, req := serviceFixture(t)
	scope := filepath.Join(req.Project.Root, "internal", "work")
	if err := os.MkdirAll(scope, 0755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(req.Project.Root, "internal", "UMCODE.md")
	original := "# Internal\r\n\r\nKeep this prose.\r\n"
	os.WriteFile(nested, []byte(original), 0640)
	setCandidateField(t, st, req, "scope_paths", []string{"internal/work"})
	if r := s.PromoteCompleted(t.Context(), req); r.Promoted != 1 {
		t.Fatalf("first=%+v", r)
	}
	rows, err := st.ListProjectMemories(t.Context(), req.Project.ID)
	if err != nil || len(rows) != 1 || rows[0].TargetPath != "internal/UMCODE.md" {
		t.Fatalf("rows=%+v %v", rows, err)
	}
	next := addCompletedWork(t, st, req.Project, "test-command", "Use go test -race ./... for this repository")
	setCandidateField(t, st, next, "scope_paths", []string{"internal/work"})
	setCandidateField(t, st, next, "replaces_memory", rows[0].ID)
	if r := s.PromoteCompleted(t.Context(), next); r.Promoted != 1 || r.Replaced != 1 {
		t.Fatalf("replace=%+v", r)
	}
	b, _ := os.ReadFile(nested)
	if !strings.HasPrefix(string(b), original) || strings.Contains(string(b), "Use go test ./...") || !strings.Contains(string(b), "Use go test -race ./...") {
		t.Fatalf("content=%q", b)
	}
	info, _ := os.Stat(nested)
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode=%v", info.Mode())
	}
	old, _ := st.GetProjectMemory(t.Context(), rows[0].ID)
	if old.Status != protocol.MemoryStatusSuperseded {
		t.Fatalf("old=%+v", old)
	}
	if _, err := os.Stat(filepath.Join(req.Project.Root, "UMCODE.md")); !os.IsNotExist(err) {
		t.Fatalf("root unexpectedly created: %v", err)
	}
	// The active identity must be found even when placement now points at root.
	conflict := addCompletedWork(t, st, req.Project, "test-command", "Use go test -count=1 ./... for this repository")
	if r := s.PromoteCompleted(t.Context(), conflict); r.Conflicted != 1 {
		t.Fatalf("cross-target=%+v", r)
	}
}

func TestPromotionDatabaseFailuresRecover(t *testing.T) {
	for _, stage := range []string{"prepare", "record", "finalize"} {
		t.Run(stage, func(t *testing.T) {
			s, st, req := serviceFixture(t)
			os.WriteFile(filepath.Join(req.Project.Root, "UMCODE.md"), []byte("before\n"), 0644)
			trigger := map[string]string{"prepare": `CREATE TRIGGER fail_promotion BEFORE INSERT ON memory_promotion_ops BEGIN SELECT RAISE(ABORT,'injected'); END`, "record": `CREATE TRIGGER fail_promotion BEFORE INSERT ON file_changes BEGIN SELECT RAISE(ABORT,'injected'); END`, "finalize": `CREATE TRIGGER fail_promotion BEFORE UPDATE OF status ON project_memories WHEN NEW.status='active' BEGIN SELECT RAISE(ABORT,'injected'); END`}[stage]
			execService(t, st, trigger)
			if r := s.PromoteCompleted(t.Context(), req); r.Pending != 1 {
				t.Fatalf("failed=%+v", r)
			}
			assertCompleted(t, st, req)
			if stage == "prepare" && readTarget(t, req.Project) != "before\n" {
				t.Fatal("prepare failure changed file")
			}
			execService(t, st, `DROP TRIGGER fail_promotion`)
			r := s.Recover(t.Context())
			if stage == "prepare" {
				r = s.PromoteCompleted(t.Context(), req)
			}
			if r.Promoted != 1 {
				t.Fatalf("retry=%+v", r)
			}
			changes, err := st.ListFileChanges(t.Context(), req.Project.ID, "", "", 100)
			if err != nil || len(changes) != 1 {
				t.Fatalf("changes=%+v %v", changes, err)
			}
		})
	}
}

func TestPromotionConcurrentPermissionChangeWins(t *testing.T) {
	s, _, req := serviceFixture(t)
	abs := filepath.Join(req.Project.Root, "UMCODE.md")
	os.WriteFile(abs, []byte("original\n"), 0644)
	s.boundary = func(at string) error {
		if at == "rename" {
			return os.Chmod(abs, 0600)
		}
		return nil
	}
	if r := s.PromoteCompleted(t.Context(), req); r.Conflicted != 1 {
		t.Fatalf("report=%+v", r)
	}
	info, err := os.Stat(abs)
	if err != nil || info.Mode().Perm() != 0600 || readTarget(t, req.Project) != "original\n" {
		t.Fatalf("external permission change overwritten: %v %v", info, err)
	}
}

func TestPromotionReadOnlyTargetRemainsPending(t *testing.T) {
	s, st, req := serviceFixture(t)
	abs := filepath.Join(req.Project.Root, "UMCODE.md")
	if err := os.WriteFile(abs, []byte("original\n"), 0444); err != nil {
		t.Fatal(err)
	}
	if r := s.PromoteCompleted(t.Context(), req); r.Pending != 1 {
		t.Fatalf("report=%+v", r)
	}
	if readTarget(t, req.Project) != "original\n" {
		t.Fatal("read-only target overwritten")
	}
	ops, err := st.ListIncompleteMemoryPromotionOps(t.Context())
	if err != nil || len(ops) != 1 || ops[0].State != protocol.MemoryOpPendingRepair {
		t.Fatalf("ops=%+v %v", ops, err)
	}
}

func TestPromotionRequalifiesAndNoCandidatesAvoidIO(t *testing.T) {
	s, st, req := serviceFixture(t)
	s.boundary = func(at string) error {
		if at == "prepare" {
			execService(t, st, `UPDATE work_nodes SET status='stale' WHERE work_id=? AND kind='fact'`, req.WorkID)
		}
		return nil
	}
	if r := s.PromoteCompleted(t.Context(), req); r.Stale != 1 {
		t.Fatalf("report=%+v", r)
	}
	if readTarget(t, req.Project) != "" {
		t.Fatal("wrote stale candidate")
	}
	execService(t, st, `DELETE FROM work_node_evidence WHERE work_id=?`, req.WorkID)
	execService(t, st, `DELETE FROM work_edges WHERE work_id=?`, req.WorkID)
	execService(t, st, `DELETE FROM work_nodes WHERE work_id=? AND kind='memory_candidate'`, req.WorkID)
	execService(t, st, `DROP TABLE memory_promotion_ops`)
	execService(t, st, `DROP TABLE project_memories`)
	s.boundary = func(string) error { t.Fatal("filesystem boundary touched"); return nil }
	req.Project.Root = "/unavailable"
	if r := s.PromoteCompleted(t.Context(), req); r != (Report{}) {
		t.Fatalf("no candidates=%+v", r)
	}
}

// Keep the fixture's candidate JSON mutation available to scoped placement tests.
func setCandidateField(t *testing.T, st *store.Store, req Request, key string, value any) {
	t.Helper()
	d, _ := st.GetWorkDetail(t.Context(), req.WorkID)
	for _, n := range d.Nodes {
		if n.Kind == protocol.NodeMemoryCandidate {
			var c map[string]any
			json.Unmarshal(n.Content, &c)
			c[key] = value
			b, _ := json.Marshal(c)
			execService(t, st, `UPDATE work_nodes SET content_json=? WHERE id=?`, string(b), n.ID)
		}
	}
}
