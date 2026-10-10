package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/fingerprint"
	"github.com/shaktsin/umcode/internal/llm"
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
	id := seedMemoryWork(t, e, th, p)
	return e, th, turn, p, id, logs
}

// seedMemoryWork observes a successful verification through the production Work
// service, then creates its candidate through the semantic client API.
func seedMemoryWork(t *testing.T, e *Engine, th protocol.Thread, p protocol.Project) string {
	return seedMemoryWorkCommand(t, e, th, p, "go test ./...")
}

func seedMemoryWorkCommand(t *testing.T, e *Engine, th protocol.Thread, p protocol.Project, command string) string {
	t.Helper()
	w, err := e.Store.CreateWork(t.Context(), protocol.Work{ThreadID: th.ID, ProjectID: p.ID, WorkflowDepth: protocol.DepthDesigned})
	if err != nil {
		t.Fatal(err)
	}
	e.Work = &work.Service{Store: e.Store, Log: e.Log, DesignedWorkflow: true, Workspace: func(context.Context, string) (fingerprint.Workspace, bool) {
		return fingerprint.Workspace{Value: "final"}, true
	}}
	commandJSON, _ := json.Marshal(map[string]string{"command": command})
	observationJSON, _ := json.Marshal(map[string]any{"results": []map[string]any{{"command": command, "status": "passed", "exit_code": 0, "output": "ok"}}})
	criterion, err := e.Store.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeCriterion, Status: protocol.StatusPending, Content: commandJSON})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Work.Observe(t.Context(), th.ID, work.Observation{Tool: "verification.run", Root: p.Root, Output: string(observationJSON)}); err != nil {
		t.Fatal(err)
	}
	observed, err := e.Store.GetWorkDetail(t.Context(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	var fact protocol.WorkNode
	var factContent work.VerifiedCommandFact
	for _, n := range observed.Nodes {
		if n.Kind == protocol.NodeFact {
			fact = n
			if err := json.Unmarshal(n.Content, &factContent); err != nil {
				t.Fatal(err)
			}
		}
	}
	if fact.ID == "" || factContent.EvidenceID == "" {
		t.Fatal("successful production observation created no durable fact")
	}
	ev, err := e.Store.AddEvidence(t.Context(), protocol.Evidence{WorkID: w.ID, NodeID: criterion.ID, SourceRevision: "final", Summary: "PRIVATE EVIDENCE BODY"})
	if err != nil {
		t.Fatal(err)
	}

	decision, err := e.Store.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeDecision, Status: protocol.StatusApproved})
	if err != nil {
		t.Fatal(err)
	}
	option, err := e.Store.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeOption, Status: "active", Content: json.RawMessage(`{"solution_rung":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	task, err := e.Store.AddWorkNode(t.Context(), protocol.WorkNode{WorkID: w.ID, Kind: protocol.NodeTask, Status: protocol.StatusCompleted})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(`INSERT INTO work_node_evidence(work_id,node_id,evidence_id) VALUES(?,?,?)`, w.ID, decision.ID, ev.ID); err != nil {
		t.Fatal(err)
	}
	for _, edge := range []protocol.WorkEdge{{WorkID: w.ID, FromNodeID: decision.ID, ToNodeID: option.ID, Relation: protocol.RelSelects}, {WorkID: w.ID, FromNodeID: criterion.ID, ToNodeID: decision.ID, Relation: protocol.RelVerifies}, {WorkID: w.ID, FromNodeID: criterion.ID, ToNodeID: task.ID, Relation: protocol.RelVerifies}} {
		if err := e.Store.AddWorkEdge(t.Context(), edge); err != nil {
			t.Fatal(err)
		}
	}
	addObservedMemoryCandidate(t, e, th, w.ID, fact, factContent)

	return w.ID
}

func addObservedMemoryCandidate(t *testing.T, e *Engine, th protocol.Thread, workID string, fact protocol.WorkNode, factContent work.VerifiedCommandFact) {
	t.Helper()
	d, err := e.Store.GetWorkDetail(t.Context(), workID)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := json.Marshal(work.MemoryCandidateContent{Category: "command", SemanticKey: "test-command", Text: "Use " + factContent.Command + " for this repository", SourceRevision: factContent.SourceRevision})
	_, _, err = e.Work.Update(t.Context(), th.ID, protocol.WorkUpdateRequest{WorkID: workID, ExpectedRevision: d.Work.Revision, Nodes: []protocol.WorkNodeChange{{Ref: "candidate", Title: "Repository test command", Kind: protocol.NodeMemoryCandidate, ToStatus: protocol.StatusPending, Content: content, EvidenceIDs: []string{factContent.EvidenceID}}}, Edges: []protocol.WorkEdgeChange{{From: "candidate", To: fact.ID, Relation: protocol.RelCandidateFor}}})
	if err != nil {
		t.Fatal(err)
	}
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
	e, th, turn, p, id, logs := memoryEngine(t)
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

	foundRecovery := false
	for _, line := range bytes.Split(logs.Bytes(), []byte("\n")) {
		var data map[string]any
		if json.Unmarshal(line, &data) != nil || data["msg"] != "memory recovery diagnostic" {
			continue
		}
		foundRecovery = true
		if data["work_id"] != id || data["target_path"] != "UMCODE.md" || data["reason"] != "recovery_completed" || data["recovery"] != "completed" {
			t.Fatalf("recovery metadata=%s", line)
		}
	}
	if !foundRecovery {
		t.Fatalf("missing recovery provenance: %s", logs)
	}
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

// Catches reducing safe service diagnostics to aggregate counts only.
func TestMemoryDiagnosticsLogRequiredMetadata(t *testing.T) {
	e, th, turn, p, id, logs := memoryEngine(t)
	e.initMemory(t.Context())
	e.finishTurn(tools.WithScope(t.Context(), &tools.Scope{Root: p.Root}), th, turn, nil, func() {})
	found := false
	for _, line := range bytes.Split(logs.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal(line, &data); err != nil {
			t.Fatal(err)
		}
		if data["msg"] != "memory promotion diagnostic" {
			continue
		}
		found = true
		for _, key := range []string{"project_id", "work_id", "candidate_node_id", "source_node_ids", "source_revision", "evidence_ids", "operation_id", "thread_id", "turn_id", "target_path", "before_hash", "after_hash", "bytes_before", "bytes_after", "status", "reason", "inserted", "replaced", "unchanged", "conflicts", "estimated_instruction_tokens_added", "estimated_context_tokens_avoided"} {
			if _, ok := data[key]; !ok {
				t.Errorf("missing %s: %s", key, line)
			}
		}
		if data["work_id"] != id || data["target_path"] != "UMCODE.md" || data["reason"] != "inserted" || data["status"] != "promoted" {
			t.Fatalf("metadata=%s", line)
		}
		after := data["bytes_after"].(float64)
		if data["estimated_instruction_tokens_added"] != float64((int(after)+3)/4) || data["estimated_context_tokens_avoided"] != float64(0) {
			t.Fatalf("accounting=%s", line)
		}
	}
	if !found {
		t.Fatalf("no per-operation diagnostic: %s", logs)
	}
	if strings.Contains(logs.String(), "Use go test") || strings.Contains(logs.String(), "PRIVATE") || strings.Contains(logs.String(), "test-command") {
		t.Fatalf("semantic diagnostic=%s", logs)
	}
}

func TestMemoryRecoveryEmissionFailureUsesRecoveryCounter(t *testing.T) {
	e, _, _, _, _, logs := memoryEngine(t)
	e.initMemory(t.Context())
	if e.Memory.EmitRecovery == nil {
		t.Fatal("recovery emission attribution missing")
	}
	e.Memory.EmitRecovery(protocol.FileChangeData{TurnID: "missing_turn", Path: "UMCODE.md"})
	if e.memoryRecoveryFailures.Load() != 1 || e.memoryPromotionFailures.Load() != 0 {
		t.Fatalf("recovery=%d promotion=%d logs=%s", e.memoryRecoveryFailures.Load(), e.memoryPromotionFailures.Load(), logs)
	}
	if !strings.Contains(logs.String(), `"operation":"recovery"`) || !strings.Contains(logs.String(), `"class":"origin_turn"`) {
		t.Fatalf("unclassified emission failure=%s", logs)
	}
}

func setMemoryCandidate(t *testing.T, e *Engine, id, text, replaces string, scopes []string) {
	t.Helper()
	b, err := json.Marshal(work.MemoryCandidateContent{Category: "command", SemanticKey: "test-command", Text: text, ScopePaths: scopes, SourceRevision: "final", ReplacesMemory: replaces})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(`UPDATE work_nodes SET content_json=? WHERE work_id=? AND kind='memory_candidate'`, string(b), id); err != nil {
		t.Fatal(err)
	}
}
func memoryRows(t *testing.T, e *Engine, project string) []protocol.ProjectMemory {
	t.Helper()
	rows, err := e.Store.ListProjectMemories(t.Context(), project)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
func memoryRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func memoryWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// Catches root-only placement, accidental nested creation, omitted prompt
// guidance, double injection, or a provider request made by promotion itself.
func TestCuratedMemoryE2EPlacementAndNextPrompt(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(map[bool]string{false: "root", true: "nearest_existing_nested"}[nested], func(t *testing.T) {
			e, th, turn, p, id, _ := memoryEngine(t)
			provider := installMemoryProvider(t, e)
			rootBefore := "# Root instructions\nUser-owned text stays intact.\n"
			memoryWrite(t, filepath.Join(p.Root, "UMCODE.md"), rootBefore)
			path := "UMCODE.md"
			prefix := rootBefore
			if nested {
				path = "pkg/UMCODE.md"
				prefix = "# Package instructions\nRetain this wording.\n"
				memoryWrite(t, filepath.Join(p.Root, path), prefix)
				memoryWrite(t, filepath.Join(p.Root, "pkg/deep/source.go"), "package deep\n")
				setMemoryCandidate(t, e, id, memoryCommandText, "", []string{"pkg/deep/source.go"})
			}
			foreign := map[string]string{"AGENTS.md": "Foreign root guidance must stay untouched.\n", "CLAUDE.md": "Foreign project guidance.\n", "pkg/deep/AGENTS.md": "Foreign nested guidance.\n"}
			for name, content := range foreign {
				memoryWrite(t, filepath.Join(p.Root, name), content)
			}
			e.initMemory(t.Context())
			runMemoryTurn(t, e, th, turn)
			assertMemoryCompletion(t, e, id)
			if len(provider.requests) != 1 {
				t.Fatalf("promotion added a model call: %d", len(provider.requests))
			}
			rows := memoryRows(t, e, p.ID)
			if len(rows) != 1 || rows[0].Status != protocol.MemoryStatusActive || rows[0].TargetPath != path {
				t.Fatalf("memory placement=%+v", rows)
			}
			want := prefix + "\n## Verified project memory\n\n<!-- umcode:generated -->\n- " + memoryCommandText + "\n"
			if got := string(memoryRead(t, filepath.Join(p.Root, path))); got != want {
				t.Fatalf("instructions=%q want=%q", got, want)
			}
			if nested {
				if got := string(memoryRead(t, filepath.Join(p.Root, "UMCODE.md"))); got != rootBefore {
					t.Fatalf("root changed=%q", got)
				}
				if _, err := os.Stat(filepath.Join(p.Root, "pkg/deep/UMCODE.md")); !os.IsNotExist(err) {
					t.Fatalf("created absent nested instructions: %v", err)
				}
			}
			for name, content := range foreign {
				if got := string(memoryRead(t, filepath.Join(p.Root, name))); got != content {
					t.Fatalf("foreign file %s changed", name)
				}
			}
			next := nextMemoryTurn(t, e, th)
			nextID := seedMemoryWork(t, e, th, p)
			calls := 0
			provider.script = func(req llm.Request) []llm.Event {
				calls++
				if nested && calls == 1 {
					if strings.Contains(req.System, memoryCommandText) {
						t.Fatal("nested guidance applied before a scoped path")
					}
					return memoryCall("file.read", `{"path":"pkg/deep/source.go"}`)
				}
				if got := strings.Count(req.System, memoryCommandText); got != 1 {
					t.Fatalf("next applicable prompt contains guidance %d times", got)
				}
				if strings.Contains(req.System, "Foreign root guidance") || strings.Contains(req.System, "Foreign project guidance") || strings.Contains(req.System, "Foreign nested guidance") {
					t.Fatal("foreign instructions entered prompt")
				}
				return memoryAnswer(req)
			}
			runMemoryTurn(t, e, th, next)
			assertMemoryCompletion(t, e, nextID)
			wantCalls := 2
			if nested {
				wantCalls = 3
			}
			if len(provider.requests) != wantCalls {
				t.Fatalf("unexpected total provider calls=%d want=%d", len(provider.requests), wantCalls)
			}
		})
	}
}

// Catches destructive replacement, lost provenance, overwriting an external
// edit, or reopening completed product verification after the instruction edit.
func TestCuratedMemoryE2EReplacementAndExternalOwnership(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact_replacement", true: "external_edit_conflicts"}[external], func(t *testing.T) {
			e, th, turn, p, id, _ := memoryEngine(t)
			provider := installMemoryProvider(t, e)
			e.initMemory(t.Context())
			runMemoryTurn(t, e, th, turn)
			assertMemoryCompletion(t, e, id)
			initial := memoryRows(t, e, p.ID)
			if len(initial) != 1 {
				t.Fatalf("initial promoted memory=%+v", initial)
			}
			old := initial[0]
			path := filepath.Join(p.Root, "UMCODE.md")
			before := memoryRead(t, path)
			if external {
				before = bytes.ReplaceAll(before, []byte("- "+memoryCommandText), []byte("- User-edited command: go test ./... -race; retain café exactly."))
				before = append(before, []byte("\nUser addition with  two spaces.\r\n")...)
				if err := os.WriteFile(path, before, 0600); err != nil {
					t.Fatal(err)
				}
			}
			next := nextMemoryTurn(t, e, th)
			replacementID := seedMemoryWorkCommand(t, e, th, p, "go test ./... -count=1")
			newText := "Use go test ./... -count=1 for this repository"
			setMemoryCandidate(t, e, replacementID, newText, old.ID, nil)
			runMemoryTurn(t, e, th, next)
			assertMemoryCompletion(t, e, replacementID)
			if len(provider.requests) != 2 {
				t.Fatalf("replacement added provider calls=%d", len(provider.requests))
			}
			rows := memoryRows(t, e, p.ID)
			if external {
				if !bytes.Equal(memoryRead(t, path), before) {
					t.Fatal("external edit overwritten")
				}
				d, err := e.Store.GetWorkDetail(t.Context(), replacementID)
				if err != nil {
					t.Fatal(err)
				}
				for _, n := range d.Nodes {
					if n.Kind == protocol.NodeMemoryCandidate && n.Status != protocol.MemoryOutcomeConflicted {
						t.Fatalf("candidate=%+v", n)
					}
				}
				if len(rows) != 1 || rows[0].ID != old.ID || rows[0].Text != old.Text {
					t.Fatalf("external conflict lost historical row=%+v", rows)
				}
			} else {
				if len(rows) != 1 {
					t.Fatalf("active replacement=%+v", rows)
				}
				active := rows[0]
				historical, err := e.Store.GetProjectMemory(t.Context(), old.ID)
				if err != nil {
					t.Fatal(err)
				}
				var total int
				if err := e.Store.DB.QueryRow(`SELECT COUNT(*) FROM project_memories WHERE project_id=?`, p.ID).Scan(&total); err != nil {
					t.Fatal(err)
				}
				if total != 2 || active.ID == old.ID || active.Text != "- "+newText || historical.Status != protocol.MemoryStatusSuperseded || historical.SupersededBy != active.ID || historical.Text != old.Text || historical.WorkID != id {
					t.Fatalf("replacement active=%+v historical=%+v total=%d", active, historical, total)
				}
				want := bytes.ReplaceAll(before, []byte(memoryCommandText), []byte(newText))
				if !bytes.Equal(memoryRead(t, path), want) {
					t.Fatal("replacement changed unrelated bytes")
				}
			}
		})
	}
}

// Catches memory writes bypassing the ordinary project recorder or undo path.
func TestCuratedMemoryE2EDiffUndoPreservesCompletedCriteria(t *testing.T) {
	e, th, turn, p, id, _ := memoryEngine(t)
	provider := installMemoryProvider(t, e)
	original := "# User guidance\r\nKeep these bytes.\r\n"
	memoryWrite(t, filepath.Join(p.Root, "UMCODE.md"), original)
	e.initMemory(t.Context())
	runMemoryTurn(t, e, th, turn)
	d := assertMemoryCompletion(t, e, id)
	diff, err := e.Projects.Diff(t.Context(), p, protocol.ProjectDiffParams{TurnID: turn.ID})
	if err != nil || len(diff.Files) != 1 || diff.Files[0].Path != "UMCODE.md" || !diff.Files[0].Revertable || !strings.Contains(diff.Files[0].Diff, "+"+"- "+memoryCommandText) {
		t.Fatalf("diff=%+v err=%v", diff, err)
	}
	undo, err := e.Projects.RevertTurn(t.Context(), turn.ID, nil)
	if err != nil || len(undo.Reverted) != 1 || undo.Reverted[0] != "UMCODE.md" || len(undo.Skipped) != 0 {
		t.Fatalf("undo=%+v err=%v", undo, err)
	}
	if got := string(memoryRead(t, filepath.Join(p.Root, "UMCODE.md"))); got != original {
		t.Fatalf("undo bytes=%q", got)
	}
	after := assertMemoryCompletion(t, e, id)
	if !reflect.DeepEqual(d, after) {
		t.Fatal("memory promotion/undo modified completed Work graph")
	}
	diff, err = e.Projects.Diff(t.Context(), p, protocol.ProjectDiffParams{TurnID: turn.ID})
	if err != nil || len(diff.Files) != 0 {
		t.Fatalf("undo still in diff=%+v err=%v", diff, err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("promotion/undo added model calls=%d", len(provider.requests))
	}
}

// Simulate interruption after the atomic rename by failing history persistence,
// then close/reopen SQLite and construct a new engine for actual startup repair.
func TestCuratedMemoryE2ERestartAfterRename(t *testing.T) {
	e, th, turn, p, id, _ := memoryEngine(t)
	provider := installMemoryProvider(t, e)
	e.initMemory(t.Context())
	if _, err := e.Store.DB.Exec(`CREATE TRIGGER fail_history BEFORE INSERT ON file_changes BEGIN SELECT RAISE(ABORT,'injected crash boundary'); END`); err != nil {
		t.Fatal(err)
	}
	runMemoryTurn(t, e, th, turn)
	assertMemoryCompletion(t, e, id)
	written := memoryRead(t, filepath.Join(p.Root, "UMCODE.md"))
	ops, err := e.Store.ListIncompleteMemoryPromotionOps(t.Context())
	if err != nil || len(ops) != 1 || !bytes.Equal(ops[0].AfterBytes, written) {
		t.Fatalf("post-rename operation=%+v err=%v", ops, err)
	}
	if _, err := e.Store.DB.Exec(`DROP TRIGGER fail_history`); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(t.TempDir(), "restart.db")
	if _, err := e.Store.DB.Exec(`VACUUM INTO ?`, db); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	cfg := config.Default(t.TempDir())
	cfg.Models.DesignedWorkflow = true
	cfg.Memory.AutoPromote = true
	recovered, err := New(t.Context(), Options{Config: cfg, Store: reopened, Secrets: secrets.NewFileStore(filepath.Join(cfg.Home, "secrets.json")), Logger: e.Log, DisableMCP: true, DisableScheduler: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { recovered.Shutdown(context.Background()) })
	recovered.LLMs.Register(provider)
	recovered.recoverMemory(t.Context())
	rows := memoryRows(t, recovered, p.ID)
	if len(rows) != 1 || rows[0].Status != protocol.MemoryStatusActive {
		t.Fatalf("recovered memories=%+v", rows)
	}
	var count int
	if err := reopened.DB.QueryRow(`SELECT COUNT(*) FROM file_changes WHERE promotion_op_id=?`, ops[0].ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("recovered history count=%d err=%v", count, err)
	}
	if err := reopened.DB.QueryRow(`SELECT COUNT(*) FROM memory_promotion_ops WHERE id=? AND state='committed'`, ops[0].ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("operation not committed count=%d err=%v", count, err)
	}
	if err := reopened.DB.QueryRow(`SELECT COUNT(*) FROM llm_usage`).Scan(&count); err != nil || count != 1 || len(provider.requests) != 1 {
		t.Fatalf("restart added model call usage=%d calls=%d err=%v", count, len(provider.requests), err)
	}
	if !bytes.Equal(memoryRead(t, filepath.Join(p.Root, "UMCODE.md")), written) {
		t.Fatal("recovery rewrote post-rename bytes")
	}
	d := assertMemoryCompletion(t, recovered, id)
	for _, n := range d.Nodes {
		if n.Kind == protocol.NodeMemoryCandidate && n.Status != protocol.MemoryOutcomePromoted {
			t.Fatalf("recovered candidate=%+v", n)
		}
	}
}
