package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/fingerprint"
	"github.com/shaktsin/umcode/internal/memory"
	"github.com/shaktsin/umcode/internal/projects"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/secrets"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/tools"
	"github.com/shaktsin/umcode/internal/work"
)

// The real completed Work, history rows and bus events catch promotion before
// CloseWork commits, promotion after publication, and attribution to a new turn.
func memoryEngine(t *testing.T) (*Engine, protocol.Thread, protocol.Turn, protocol.Project, string, *bytes.Buffer) {
	t.Helper()
	e, th, turn, st := pluginHookEngine(t)
	logs := new(bytes.Buffer)
	e.Log = slog.New(slog.NewJSONHandler(logs, nil))
	e.Cfg.Models.DesignedWorkflow, e.Cfg.Memory.AutoPromote = true, true
	e.Cfg.Memory.TargetFileBytes = 4096
	e.Projects = projects.New(st, e.Cfg)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProject(t.Context(), protocol.Project{Root: root, Name: "memory"})
	if err != nil {
		t.Fatal(err)
	}
	if err := func() error {
		_, err := st.DB.Exec(`UPDATE threads SET project_id=? WHERE id=?`, p.ID, th.ID)
		return err
	}(); err != nil {
		t.Fatal(err)
	}
	th.ProjectID = p.ID
	w, err := st.CreateWork(t.Context(), protocol.Work{ThreadID: th.ID, ProjectID: p.ID, WorkflowDepth: protocol.DepthDesigned})
	if err != nil {
		t.Fatal(err)
	}
	e.Work = &work.Service{Store: st, Log: e.Log, DesignedWorkflow: true, Workspace: func(context.Context, string) (fingerprint.Workspace, bool) {
		return fingerprint.Workspace{Value: "final"}, true
	}}
	fact, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeFact, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	criterion, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeCriterion, Status: protocol.AttemptPassed})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeMemoryCandidate, Status: protocol.StatusPending, Content: json.RawMessage(`{"category":"command","semantic_key":"test-command","text":"Use go test ./... for this repository","scope_paths":[],"source_revision":"final"}`)})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := st.AddEvidence(t.Context(), protocol.Evidence{WorkID: w.ID, NodeID: criterion.ID, SourceRevision: "final", Summary: "PRIVATE EVIDENCE BODY"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO work_node_evidence(work_id,node_id,evidence_id) VALUES(?,?,?)`, w.ID, candidate.ID, ev.ID); err != nil {
		t.Fatal(err)
	}
	for _, edge := range []protocol.WorkEdge{{WorkID: w.ID, FromNodeID: candidate.ID, ToNodeID: fact.ID, Relation: protocol.RelCandidateFor}, {WorkID: w.ID, FromNodeID: criterion.ID, ToNodeID: candidate.ID, Relation: protocol.RelVerifies}} {
		if err := st.AddWorkEdge(t.Context(), edge); err != nil {
			t.Fatal(err)
		}
	}

	decision, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeDecision, Status: protocol.StatusApproved})
	if err != nil {
		t.Fatal(err)
	}
	option, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeOption, Status: "active", Content: json.RawMessage(`{"solution_rung":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeTask, Status: protocol.StatusCompleted})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO work_node_evidence(work_id,node_id,evidence_id) VALUES(?,?,?)`, w.ID, decision.ID, ev.ID); err != nil {
		t.Fatal(err)
	}
	for _, edge := range []protocol.WorkEdge{{WorkID: w.ID, FromNodeID: decision.ID, ToNodeID: option.ID, Relation: protocol.RelSelects}, {WorkID: w.ID, FromNodeID: criterion.ID, ToNodeID: decision.ID, Relation: protocol.RelVerifies}, {WorkID: w.ID, FromNodeID: criterion.ID, ToNodeID: task.ID, Relation: protocol.RelVerifies}} {
		if err := st.AddWorkEdge(t.Context(), edge); err != nil {
			t.Fatal(err)
		}
	}
	fp, err := st.AddFingerprint(t.Context(), protocol.Fingerprint{WorkID: w.ID, Kind: protocol.FingerprintVerification, Value: "final", TakenAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddVerificationAttempt(t.Context(), protocol.VerificationAttempt{WorkID: w.ID, CriterionNodeID: criterion.ID, Status: protocol.AttemptPassed, EvidenceID: ev.ID, FingerprintID: fp.ID, StartedAt: time.Now(), FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return e, th, turn, p, w.ID, logs
}

type memorySubscriber struct {
	thread string
	notify func(string, any)
}

func (s *memorySubscriber) ID() string                  { return "memory-test" }
func (s *memorySubscriber) IsAdmin() bool               { return false }
func (s *memorySubscriber) Wants(id string) bool        { return id == s.thread }
func (s *memorySubscriber) Notify(method string, p any) { s.notify(method, p) }

func TestMemoryPromotionCompletionOrderingAndHistory(t *testing.T) {
	e, th, turn, p, id, logs := memoryEngine(t)
	e.initMemory(t.Context())
	var methods []string
	released := false
	e.Bus.Add(&memorySubscriber{thread: th.ID, notify: func(method string, params any) {
		if method != protocol.NotifyItemCompleted && method != protocol.NotifyTurnCompleted {
			return
		}
		if method == protocol.NotifyItemCompleted {
			d, err := e.Store.GetWorkDetail(t.Context(), id)
			if err != nil || d.Work.Status != protocol.WorkCompleted {
				t.Errorf("promotion before Work commit: %+v %v", d.Work, err)
			}
			if released {
				t.Error("promotion after thread release")
			}
		}
		if method == protocol.NotifyTurnCompleted {
			if !released {
				t.Error("completion before thread release")
			}
			rows, err := e.Store.ListProjectMemories(t.Context(), p.ID)
			if err != nil || len(rows) != 1 {
				t.Errorf("completion before promotion: %+v %v", rows, err)
			}
		}
		methods = append(methods, method)
	}})
	ctx := tools.WithScope(t.Context(), &tools.Scope{Root: p.Root})
	e.finishTurn(ctx, th, turn, nil, func() { released = true })
	if len(methods) != 2 || methods[0] != protocol.NotifyItemCompleted || methods[1] != protocol.NotifyTurnCompleted {
		t.Fatalf("event order=%v", methods)
	}
	items, err := e.Store.ListItems(t.Context(), th.ID, 0)
	if err != nil || len(items) != 1 || items[0].Kind != protocol.ItemFileChange || items[0].TurnID != turn.ID {
		t.Fatalf("items=%+v %v", items, err)
	}
	var change protocol.FileChangeData
	if err := json.Unmarshal(items[0].Data, &change); err != nil {
		t.Fatal(err)
	}
	if change.TurnID != turn.ID || change.Path != "UMCODE.md" || !change.Revertable {
		t.Fatalf("change=%+v", change)
	}
	d, err := e.Store.GetWorkDetail(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range d.Nodes {
		if n.Kind == protocol.NodeCriterion && n.Status != protocol.AttemptPassed {
			t.Fatalf("memory write staled criterion: %+v", n)
		}
	}
	for _, ev := range d.Evidence {
		if ev.StaleAt != nil {
			t.Fatal("memory write staled completed evidence")
		}
	}
	e.finishTurn(ctx, th, turn, nil, func() {})
	var changes int
	if err := e.Store.DB.QueryRow(`SELECT COUNT(*) FROM file_changes WHERE turn_id=?`, turn.ID).Scan(&changes); err != nil || changes != 1 {
		t.Fatalf("history=%d %v", changes, err)
	}
	if strings.Contains(logs.String(), "Use go test") || strings.Contains(logs.String(), "PRIVATE") {
		t.Fatalf("semantic logs: %s", logs)
	}
	if !strings.Contains(logs.String(), "estimated_instruction_tokens_added") {
		t.Fatalf("missing metadata accounting: %s", logs)
	}
}

func TestMemoryPromotionFastPaths(t *testing.T) {
	for _, mode := range []string{"flag_off", "designed_off", "projectless", "no_candidates", "already_completed", "blocked", "failed", "interrupted", "paused"} {
		t.Run(mode, func(t *testing.T) {
			e, th, turn, p, id, logs := memoryEngine(t)
			switch mode {
			case "flag_off":
				e.Cfg.Memory.AutoPromote = false
			case "designed_off":
				e.Cfg.Models.DesignedWorkflow = false
			case "projectless":
				th.ProjectID = ""
				if _, err := e.Store.DB.Exec(`UPDATE works SET project_id=NULL WHERE id=?`, id); err != nil {
					t.Fatal(err)
				}
			case "no_candidates":
				if _, err := e.Store.DB.Exec(`UPDATE work_nodes SET status='rejected' WHERE work_id=? AND kind='memory_candidate'`, id); err != nil {
					t.Fatal(err)
				}
			case "already_completed":
				if err := e.Store.CloseWork(t.Context(), id, protocol.WorkCompleted, time.Now()); err != nil {
					t.Fatal(err)
				}
			case "blocked":
				if _, err := e.Store.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: id, Kind: protocol.NodeCriterion, Status: protocol.StatusPending}); err != nil {
					t.Fatal(err)
				}
			case "paused":
				e.markPaused(turn.ID)
			}
			e.initMemory(t.Context())
			// Invalid promotion storage and an absent project folder turn accidental
			// promotion queries or filesystem access into visible failures.
			if _, err := e.Store.DB.Exec(`DROP TABLE project_memories`); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(p.Root); err != nil {
				t.Fatal(err)
			}
			var endErr error
			if mode == "failed" {
				endErr = context.DeadlineExceeded
			}
			if mode == "interrupted" {
				endErr = context.Canceled
			}
			e.finishTurn(t.Context(), th, turn, endErr, func() {})
			if e.memoryPromotionFailures.Load() != 0 {
				t.Fatalf("fast path attempted promotion: %s", logs)
			}
			var count int
			if err := e.Store.DB.QueryRow(`SELECT COUNT(*) FROM memory_promotion_ops`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("promotion operations=%d %v", count, err)
			}
			if strings.Contains(logs.String(), "PRIVATE") || strings.Contains(logs.String(), "Use go test") {
				t.Fatalf("semantic diagnostic=%s", logs)
			}
			if mode == "designed_off" && strings.Count(logs.String(), "memory promotion inactive") != 1 {
				t.Fatalf("missing single inactive diagnostic: %s", logs)
			}
		})
	}
}

func TestMemoryPromotionFailureDoesNotChangeCompletion(t *testing.T) {
	for _, mode := range []string{"error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			e, th, turn, p, id, logs := memoryEngine(t)
			e.initMemory(t.Context())
			if mode == "error" {
				if _, err := e.Store.DB.Exec(`CREATE TRIGGER fail_memory BEFORE INSERT ON memory_promotion_ops BEGIN SELECT RAISE(ABORT,'PRIVATE FAILURE BODY'); END`); err != nil {
					t.Fatal(err)
				}
			} else {
				e.Memory.Emit = func(protocol.FileChangeData) { panic("PRIVATE PANIC BODY") }
			}
			published := false
			e.Bus.Add(&memorySubscriber{thread: th.ID, notify: func(method string, _ any) {
				if method == protocol.NotifyTurnCompleted {
					published = true
				}
			}})
			e.finishTurn(tools.WithScope(t.Context(), &tools.Scope{Root: p.Root}), th, turn, nil, func() {})
			d, err := e.Store.GetWorkDetail(t.Context(), id)
			if err != nil || d.Work.Status != protocol.WorkCompleted {
				t.Fatalf("Work=%+v %v", d.Work, err)
			}
			turns, err := e.Store.ListTurns(t.Context(), th.ID)
			if err != nil || len(turns) != 1 || turns[0].Status != protocol.TurnCompleted || !published {
				t.Fatalf("turns=%+v published=%v err=%v", turns, published, err)
			}
			if e.memoryPromotionFailures.Load() != 1 {
				t.Fatalf("failure count=%d logs=%s", e.memoryPromotionFailures.Load(), logs)
			}
			if strings.Contains(logs.String(), "PRIVATE") || strings.Contains(logs.String(), "Use go test") {
				t.Fatalf("unsafe logs=%s", logs)
			}
			if !strings.Contains(logs.String(), `"class":"`+map[string]string{"error": "pending", "panic": "panic"}[mode]+`"`) {
				t.Fatalf("missing safe failure class: %s", logs)
			}
		})
	}
}

func TestMemoryRecoveryStartupGateAndFailure(t *testing.T) {
	for _, auto := range []bool{false, true} {
		for _, designed := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{true: "auto", false: "off"}[auto], map[bool]string{true: "designed", false: "legacy"}[designed]}, "_"), func(t *testing.T) {
				st, err := store.Open(t.Context(), ":memory:")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { st.Close() })
				if _, err := st.DB.Exec(`DROP TABLE memory_promotion_ops`); err != nil {
					t.Fatal(err)
				}
				cfg := config.Default(t.TempDir())
				cfg.Memory.AutoPromote = auto
				cfg.Models.DesignedWorkflow = designed
				logs := new(bytes.Buffer)
				e, err := New(t.Context(), Options{Config: cfg, Store: st, Secrets: secrets.NewFileStore(filepath.Join(cfg.Home, "secrets.json")), Logger: slog.New(slog.NewJSONHandler(logs, nil)), DisableMCP: true, DisableScheduler: true})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { e.Shutdown(context.Background()) })
				if (e.Memory != nil) != (auto && designed) {
					t.Fatalf("service activated=%v", e.Memory != nil)
				}
				want := int64(0)
				if auto && designed {
					want = 1
				}
				if e.memoryRecoveryFailures.Load() != want {
					t.Fatalf("recovery failures=%d want %d logs=%s", e.memoryRecoveryFailures.Load(), want, logs)
				}
				if auto && !designed && strings.Count(logs.String(), "memory promotion inactive") != 1 {
					t.Fatalf("diagnostic=%s", logs)
				}
			})
		}
	}
}

func TestMemoryRecoveryPanicIsContained(t *testing.T) {
	e, _, _, _, _, logs := memoryEngine(t)
	e.Memory = memory.New(nil, e.Projects, 4096)
	e.recoverMemory(t.Context())
	if e.memoryRecoveryFailures.Load() != 1 || strings.Contains(logs.String(), "runtime") || !strings.Contains(logs.String(), `"class":"panic"`) {
		t.Fatalf("unsafe panic handling=%s", logs)
	}
}

func TestMemoryRecoveryStartupPublishesOriginalTurn(t *testing.T) {
	e, th, turn, p, id, _ := memoryEngine(t)
	e.initMemory(t.Context())
	if _, err := e.Store.DB.Exec(`CREATE TRIGGER fail_history BEFORE INSERT ON file_changes BEGIN SELECT RAISE(ABORT,'PRIVATE HISTORY BODY'); END`); err != nil {
		t.Fatal(err)
	}
	e.finishTurn(tools.WithScope(t.Context(), &tools.Scope{Root: p.Root}), th, turn, nil, func() {})
	ops, err := e.Store.ListIncompleteMemoryPromotionOps(t.Context())
	if err != nil || len(ops) != 1 {
		t.Fatalf("incomplete=%+v %v", ops, err)
	}
	if _, err := e.Store.DB.Exec(`DROP TRIGGER fail_history`); err != nil {
		t.Fatal(err)
	}
	// A new chat/turn must not receive the old completion's recovered edit.
	other, err := e.Store.CreateThread(t.Context(), protocol.Thread{Title: "other", ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	newer := protocol.Turn{ID: store.NewID("trn"), ThreadID: other.ID, Status: protocol.TurnRunning}
	if err := e.Store.CreateTurn(t.Context(), newer); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default(t.TempDir())
	cfg.Models.DesignedWorkflow = true
	cfg.Memory.AutoPromote = true
	recovered, err := New(t.Context(), Options{Config: cfg, Store: e.Store, Secrets: secrets.NewFileStore(filepath.Join(cfg.Home, "secrets.json")), Logger: e.Log, DisableMCP: true, DisableScheduler: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { recovered.Shutdown(context.Background()) })
	if recovered.memoryRecoveryFailures.Load() != 0 {
		t.Fatal("recovery failed")
	}
	ops, err = e.Store.ListIncompleteMemoryPromotionOps(t.Context())
	if err != nil || len(ops) != 0 {
		t.Fatalf("still incomplete=%+v %v", ops, err)
	}
	items, err := e.Store.ListItems(t.Context(), th.ID, 0)
	if err != nil || len(items) != 1 || items[0].TurnID != turn.ID || items[0].Kind != protocol.ItemFileChange {
		t.Fatalf("original history=%+v %v", items, err)
	}
	otherItems, err := e.Store.ListItems(t.Context(), other.ID, 0)
	if err != nil || len(otherItems) != 0 {
		t.Fatalf("foreign history=%+v %v", otherItems, err)
	}
	recovered.recoverMemory(t.Context())
	items, err = e.Store.ListItems(t.Context(), th.ID, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("recovery emitted twice=%+v %v", items, err)
	}
	d, err := e.Store.GetWorkDetail(t.Context(), id)
	if err != nil || d.Work.Status != protocol.WorkCompleted {
		t.Fatalf("Work=%+v %v", d.Work, err)
	}
	turns, err := e.Store.ListTurns(t.Context(), other.ID)
	if err != nil || len(turns) != 1 || turns[0].Status != protocol.TurnInterrupted {
		t.Fatalf("housekeeping not before recovery: %+v %v", turns, err)
	}
}
