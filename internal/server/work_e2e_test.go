package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/client"
	"github.com/shaktsin/umcode/internal/config"
	"github.com/shaktsin/umcode/internal/engine"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/server"
	"github.com/shaktsin/umcode/internal/store"
)

func startWorkThread(h *harness) protocol.Thread {
	var th protocol.Thread
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: h.proj.ID, Title: "work"}, &th)
	return th
}

func runWorkTurn(h *harness, th protocol.Thread, text string) protocol.Turn {
	var res protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: text}, &res)
	turn, _, _ := h.waitTurn(res.Turn.ID, nil)
	return turn
}

func listWorks(h *harness, threadID string) []protocol.Work {
	var out protocol.WorkListResult
	h.call(protocol.MethodWorkList, protocol.WorkListParams{ThreadID: threadID}, &out)
	return out.Works
}

func TestWorkListEmptyThread(t *testing.T) {
	h := newHarness(t, nil)
	th := startWorkThread(h)
	var raw json.RawMessage
	h.call(protocol.MethodWorkList, protocol.WorkListParams{ThreadID: th.ID}, &raw)
	if string(raw) != `{"works":[]}` {
		t.Fatalf("work/list = %s", raw)
	}
}

func TestWorkRecordedForEditTurn(t *testing.T) {
	h := newHarness(t, nil)
	h.addKey("claude", "k", "sk-1")
	h.fake.push(
		toolReply("file__write", `{"path":"notes.txt","content":"hi\n"}`),
		textReply("Done."),
	)
	th := startWorkThread(h)
	if turn := runWorkTurn(h, th, "write notes"); turn.Status != protocol.TurnCompleted {
		t.Fatalf("turn = %+v", turn)
	}
	works := listWorks(h, th.ID)
	if len(works) != 1 {
		t.Fatalf("works = %+v", works)
	}
	w := works[0]
	if w.Status != protocol.WorkCompleted || w.WorkflowDepth != protocol.DepthGuided || w.Goal != "write notes" {
		t.Fatalf("work = %+v", w)
	}
	var d protocol.WorkDetail
	h.call(protocol.MethodWorkGet, protocol.WorkGetParams{WorkID: w.ID}, &d)
	artifacts, changes := 0, 0
	for _, n := range d.Nodes {
		if n.Kind == protocol.NodeArtifact && n.Title == "notes.txt" {
			artifacts++
		}
	}
	for _, ev := range d.Evidence {
		if ev.Kind == protocol.EvidenceFileChange {
			changes++
		}
	}
	if artifacts != 1 || changes != 1 {
		t.Fatalf("artifacts = %d, file changes = %d: %+v", artifacts, changes, d)
	}
}

func TestWorkContinuesAcrossTurnsUntilResolved(t *testing.T) {
	h := newHarness(t, nil)
	h.addKey("claude", "k", "sk-1")
	h.fake.push(textReply("First."), textReply("Second."))
	th := startWorkThread(h)
	runWorkTurn(h, th, "one")
	runWorkTurn(h, th, "two")
	if works := listWorks(h, th.ID); len(works) != 2 || works[0].Goal != "two" {
		t.Fatalf("resolved works should not be reused, newest first: %+v", works)
	}

	h2 := newHarness(t, nil)
	h2.addKey("claude", "k", "sk-1")
	h2.fake.push(
		func(llm.Request, string) ([]llm.Event, error) { return nil, context.Canceled },
		textReply("Resumed."),
	)
	th2 := startWorkThread(h2)
	if turn := runWorkTurn(h2, th2, "start"); turn.Status != protocol.TurnInterrupted {
		t.Fatalf("turn = %+v", turn)
	}
	if works := listWorks(h2, th2.ID); len(works) != 1 || works[0].Status != protocol.WorkOpen {
		t.Fatalf("interrupted turn must leave its work open: %+v", works)
	}
	runWorkTurn(h2, th2, "continue")
	if works := listWorks(h2, th2.ID); len(works) != 1 || works[0].Status != protocol.WorkCompleted {
		t.Fatalf("next turn should continue and close the same work: %+v", works)
	}
}

func TestWorkGetUnknown(t *testing.T) {
	h := newHarness(t, nil)
	err := h.callErr(protocol.MethodWorkGet, protocol.WorkGetParams{WorkID: "nope"})
	if !containsAny(err.Error(), "not found", "invalid params") {
		t.Fatalf("err = %v", err)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

func TestChatOutputUnchanged(t *testing.T) {
	h := newHarness(t, nil)
	h.addKey("claude", "k", "sk-1")
	h.fake.push(textReply("Hello."))
	th := startWorkThread(h)
	runWorkTurn(h, th, "hi")
	var items protocol.ThreadReadResult
	h.call(protocol.MethodThreadRead, protocol.ThreadIDParams{ThreadID: th.ID}, &items)
	if len(items.Items) != 2 || items.Items[0].Kind != protocol.ItemUserMessage || items.Items[1].Kind != protocol.ItemAgentMessage {
		t.Fatalf("items = %+v", items.Items)
	}
}

func designedOn(c *config.Config) {
	c.Models.DesignedWorkflow = true
	c.Models.ContextCompiler = true
}

// Only the provider is scripted. Graph writes, approval transport, execution,
// verification and persistence all use the production engine and socket API.
func workflowFixture(t *testing.T, goal string, project bool, history bool) (*harness, protocol.Thread, protocol.WorkDetail) {
	t.Helper()
	h := newHarness(t, designedOn)
	h.addKey("claude", "workflow", "sk-1")
	var th protocol.Thread
	projectID := ""
	if project {
		projectID = h.proj.ID
		if err := os.WriteFile(filepath.Join(h.ws, "go.mod"), []byte("module acceptance\n\ngo 1.24\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(h.ws, "notes_test.go"), []byte("package acceptance\nimport (\"os\";\"testing\")\nfunc TestNotes(t *testing.T) { b,e:=os.ReadFile(\"notes.txt\"); if e!=nil || len(b)==0 {t.Fatal(\"notes missing\")} }\n"), 0600); err != nil {
			t.Fatal(err)
		}
		h.call(protocol.MethodProjectUpdate, protocol.ProjectUpdateParams{ProjectID: projectID,
			Tools: &protocol.ProjectTools{Network: boolPtr(true)}}, &h.proj)
	}
	h.call(protocol.MethodThreadStart, protocol.ThreadStartParams{ProjectID: projectID, Title: "workflow"}, &th)
	h.call(protocol.MethodThreadSetSettings, protocol.ThreadSetSettingsParams{ThreadID: th.ID,
		Settings: protocol.ModelSelection{Provider: "claude", Model: "claude-sonnet-5", Complexity: "standard"}}, &th)
	h.fake.push(toolReply("verification__plan", `{}`), textReply("Inspected acceptance checks."))
	if turn := runWorkTurn(h, th, goal); turn.Status != protocol.TurnCompleted {
		t.Fatalf("fixture turn = %+v", turn)
	}
	d := workflowDetail(h, th.ID)
	workflowNode(t, d, "criterion", "")
	if len(d.Evidence) == 0 || d.Evidence[0].Kind != "discovery" {
		t.Fatalf("successful discovery did not bootstrap evidence: %+v", d.Evidence)
	}
	if history {
		for i := 0; i < 12; i++ {
			h.fake.push(textReply(strings.Repeat("Inspected context for this change. ", 120)))
			if turn := runWorkTurn(h, th, "continue inspection"); turn.Status != protocol.TurnCompleted {
				t.Fatalf("history turn = %+v", turn)
			}
		}
	}
	return h, th, workflowDetail(h, th.ID)
}

func workflowDetail(h *harness, threadID string) protocol.WorkDetail {
	h.t.Helper()
	works := listWorks(h, threadID)
	if len(works) != 1 {
		h.t.Fatalf("expected one continuing work: %+v", works)
	}
	var d protocol.WorkDetail
	h.call(protocol.MethodWorkGet, protocol.WorkGetParams{WorkID: works[0].ID}, &d)
	return d
}

func workflowNode(t *testing.T, d protocol.WorkDetail, kind, title string) protocol.WorkNode {
	t.Helper()
	for _, n := range d.Nodes {
		if n.Kind == kind && (title == "" || n.Title == title || n.Title == "") {
			return n
		}
	}
	t.Fatalf("missing %s %q in %+v", kind, title, d.Nodes)
	return protocol.WorkNode{}
}

func workflowUpdate(h *harness, th protocol.Thread, build func(protocol.WorkDetail) protocol.WorkUpdateRequest) func(llm.Request, string) ([]llm.Event, error) {
	return func(req llm.Request, key string) ([]llm.Event, error) {
		d := modelWorkflowDetail(h.t, req)
		update := build(d)
		update.WorkID, update.ExpectedRevision = d.Work.ID, d.Work.Revision
		raw, err := json.Marshal(update)
		if err != nil {
			return nil, err
		}
		return toolReply("work__update", string(raw))(req, key)
	}
}

// Provider scripts may only use identities that a real model receives.
func modelWorkflowDetail(t *testing.T, req llm.Request) protocol.WorkDetail {
	t.Helper()
	const marker = "Canonical workflow identities (metadata only):\n"
	_, raw, ok := strings.Cut(req.System, marker)
	if !ok {
		t.Fatal("model request omitted canonical workflow identities")
	}
	var d protocol.WorkDetail
	if err := json.Unmarshal([]byte(strings.SplitN(raw, "\n\n", 2)[0]), &d); err != nil {
		t.Fatalf("workflow identities: %v", err)
	}
	return d
}

func TestDesignedWorkflowCanonicalIdentitiesBootstrapAndRefresh(t *testing.T) {
	for _, compiler := range []bool{false, true} {
		t.Run(fmt.Sprintf("compiler_%t", compiler), func(t *testing.T) {
			h := newHarness(t, func(c *config.Config) { designedOn(c); c.Models.ContextCompiler = compiler })
			h.addKey("claude", "workflow", "sk-1")
			th := startWorkThread(h)
			var firstRevision int
			h.fake.push(
				workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest {
					if d.Work.ID == "" || d.Work.Revision < 1 {
						t.Fatal("missing work identity/revision")
					}
					firstRevision = d.Work.Revision
					goal := workflowNode(t, d, protocol.NodeGoal, "")
					return protocol.WorkUpdateRequest{WorkflowDepth: "guided", Nodes: []protocol.WorkNodeChange{{Ref: "requirement", Kind: "requirement", Title: "Private acceptance prose"}}, Edges: []protocol.WorkEdgeChange{{From: goal.ID, Relation: "requires", To: "requirement"}}}
				}),
				workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest {
					if d.Work.Revision <= firstRevision {
						t.Fatal("stale work identity context")
					}
					for _, n := range d.Nodes {
						if n.Kind == "requirement" {
							if n.Title != "" || len(n.Content) != 0 {
								t.Fatal("identity context duplicated semantic prose")
							}
							return protocol.WorkUpdateRequest{Nodes: []protocol.WorkNodeChange{{ID: n.ID, ExpectedRevision: n.Revision, FromStatus: n.Status, ToStatus: "superseded"}}}
						}
					}
					t.Fatal("created node identity missing from next request")
					return protocol.WorkUpdateRequest{}
				}), textReply("Recorded."),
			)
			if turn := runWorkTurn(h, th, "record the acceptance requirement"); turn.Status != protocol.TurnCompleted {
				t.Fatalf("turn=%+v", turn)
			}
			d := workflowDetail(h, th.ID)
			if n := workflowNode(t, d, "requirement", ""); n.Status != "superseded" {
				t.Fatalf("transition using model identities failed: %+v", n)
			}
		})
	}
}

func solutionBatch(t *testing.T, d protocol.WorkDetail, gated bool, suffix string) protocol.WorkUpdateRequest {
	t.Helper()
	c := workflowNode(t, d, protocol.NodeCriterion, "")
	status, gate := "approved", ""
	if gated {
		status, gate = "proposed", `,"gate_kind":"public_contract"`
	}
	depth := d.Work.WorkflowDepth
	if depth == protocol.DepthDirect {
		depth = protocol.DepthGuided
	}
	return protocol.WorkUpdateRequest{WorkflowDepth: depth, Nodes: []protocol.WorkNodeChange{
		{Ref: "option", Kind: "option", Title: "Minimum new code" + suffix, Content: json.RawMessage(`{"solution_rung":6}`)},
		{Ref: "decision", Kind: "decision", Title: "Use a small implementation" + suffix, ToStatus: status,
			Content: json.RawMessage(`{"required":true` + gate + `}`), EvidenceIDs: []string{d.Evidence[0].ID}},
		{Ref: "task", Kind: "task", Title: "Write notes" + suffix, Content: json.RawMessage(`{"required":true}`)},
	}, Edges: []protocol.WorkEdgeChange{
		{From: "decision", Relation: "selects", To: "option"},
		{From: "task", Relation: "implements", To: "decision"},
		{From: c.ID, Relation: "verifies", To: "decision"},
		{From: c.ID, Relation: "verifies", To: "task"},
	}}
}

func taskTransition(t *testing.T, d protocol.WorkDetail, title, to string) protocol.WorkUpdateRequest {
	for i := len(d.Nodes) - 1; i >= 0; i-- {
		n := d.Nodes[i]
		if n.Kind == "task" && (n.Title == title || n.Title == "") && (n.Status == "ready" || n.Status == "in_progress") {
			return protocol.WorkUpdateRequest{Nodes: []protocol.WorkNodeChange{{ID: n.ID, ExpectedRevision: n.Revision, FromStatus: n.Status, ToStatus: to}}}
		}
	}
	t.Fatalf("no runnable task %q", title)
	return protocol.WorkUpdateRequest{}
}

func workflowTurn(h *harness, th protocol.Thread, approve func(protocol.Approval) bool) (protocol.Turn, []protocol.Item) {
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "continue"}, &started)
	var items []protocol.Item
	timeout := time.After(10 * time.Second)
	for {
		select {
		case <-timeout:
			h.t.Fatal("timed out waiting for workflow turn")
		case n := <-h.c.Notifications():
			switch n.Method {
			case protocol.NotifyItemCompleted:
				var ev protocol.ItemEvent
				if err := json.Unmarshal(n.Params, &ev); err != nil {
					h.t.Fatal(err)
				}
				if ev.Item.TurnID == started.Turn.ID && ev.Item.Kind == protocol.ItemToolCall {
					items = append(items, ev.Item)
				}
			case protocol.NotifyApprovalRequest:
				var ev protocol.ApprovalEvent
				if err := json.Unmarshal(n.Params, &ev); err != nil {
					h.t.Fatal(err)
				}
				ok := approve != nil && approve(ev.Approval)
				h.call(protocol.MethodApprovalRespond, protocol.ApprovalRespondParams{ApprovalID: ev.Approval.ID, Approve: ok, Remember: ev.Approval.Kind == "workflow"}, nil)
			case protocol.NotifyTurnCompleted:
				var ev protocol.TurnEvent
				if err := json.Unmarshal(n.Params, &ev); err != nil {
					h.t.Fatal(err)
				}
				if ev.Turn.ID == started.Turn.ID {
					return ev.Turn, items
				}
			}
		}
	}
}

func assertWorkflowApproval(t *testing.T, h *harness, th protocol.Thread, a protocol.Approval, decisionTitle, taskTitle string) {
	t.Helper()
	d := workflowDetail(h, th.ID)
	n, task := workflowNode(t, d, "decision", decisionTitle), workflowNode(t, d, "task", taskTitle)
	if a.Kind != "workflow" || a.WorkID != d.Work.ID || a.NodeID != n.ID || a.NodeRevision != n.Revision ||
		a.Tool != "work.update" || a.Status != "pending" || n.Status != "proposed" || task.Status == "ready" {
		t.Fatalf("approval does not identify the blocked graph: %+v, decision=%+v task=%+v", a, n, task)
	}
	if _, err := os.Stat(filepath.Join(h.ws, "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("mutation executed before approval: %v", err)
	}
}

// Catches missing solution/criterion/evidence links, graph completion bypass,
// accidental Guided approvals, and failed propagation into compiled requests.
func TestGuidedWorkflowRecordsSolutionTaskAndVerification(t *testing.T) {
	h, th, _ := workflowFixture(t, "write notes and verify the change", true, true)
	log := captureWorkflowRequests(h)
	h.fake.push(
		workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest { return solutionBatch(t, d, false, "") }),
		workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest {
			return taskTransition(t, d, "Write notes", "in_progress")
		}),
		toolReply("file__write", `{"path":"notes.txt","content":"verified notes\n"}`),
		toolReply("verification__run", `{"checks":[{"label":"go test","command":"go test ./...","reason":"Acceptance contract"},{"label":"go vet","command":"go vet ./...","reason":"Static checks"}]}`),
		workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest {
			return taskTransition(t, d, "Write notes", "completed")
		}),
		textReply("Verified."),
	)
	workflowApprovals := 0
	turn, items := workflowTurn(h, th, func(a protocol.Approval) bool {
		if a.Kind == "workflow" {
			workflowApprovals++
		}
		return true
	})
	d := workflowDetail(h, th.ID)
	if turn.Status != protocol.TurnCompleted || d.Work.Status != protocol.WorkCompleted || d.Work.WorkflowDepth != "guided" || workflowApprovals != 0 {
		t.Fatalf("completion=%+v work=%+v approvals=%d", turn, d.Work, workflowApprovals)
	}
	if len(items) != 5 {
		t.Fatalf("tool executions = %+v", items)
	}
	for _, item := range items {
		if item.Status != protocol.ItemCompleted {
			t.Fatalf("failed tool = %+v", item.Tool)
		}
	}
	if len(d.Attempts) != 2 || d.Attempts[0].Status != "passed" || d.Attempts[0].CriterionNodeID == "" || d.Attempts[0].EvidenceID == "" {
		t.Fatalf("verification=%+v", d.Attempts)
	}
	if n := workflowNode(t, d, "task", "Write notes"); n.Status != "completed" {
		t.Fatalf("task=%+v", n)
	}
	assertWorkflowPacket(t, h, workflowNode(t, d, "decision", "").ID, "guided", "rung=6")
	breakdowns := log.breakdowns(t)
	if len(breakdowns) != 6 {
		t.Fatalf("workflow added model calls: %d", len(breakdowns))
	}
	b := breakdowns[len(breakdowns)-1]
	if b.WorkUpdateSpecTokens <= 0 || b.WorkUpdateCallTokens <= 0 || b.WorkUpdateResultTokens <= 0 || b.P0PacketTokens <= 0 || b.P1PacketTokens <= 0 || b.P0PacketTokens+b.P1PacketTokens != b.WorkPacketTokens+b.EvidencePacketTokens {
		t.Fatalf("workflow accounting=%+v", b)
	}
}

// Catches bypassing initial Designed readiness and waking mutation before the
// approval transaction updates the node, dependent task and work revisions.
func TestDesignedWorkflowBlocksMutationUntilApproval(t *testing.T) {
	h, th, initial := workflowFixture(t, "update the public schema", true, false)
	if initial.Work.WorkflowDepth != "designed" {
		t.Fatalf("depth=%s", initial.Work.WorkflowDepth)
	}
	var pending protocol.WorkDetail
	h.fake.push(
		toolReply("file__write", `{"path":"notes.txt","content":"premature"}`),
		toolReply("file__list", `{}`),
		workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest { return solutionBatch(t, d, true, "") }),
		func(req llm.Request, key string) ([]llm.Event, error) {
			d := workflowDetail(h, th.ID)
			decision, task := workflowNode(t, d, "decision", ""), workflowNode(t, d, "task", "")
			if decision.Status != "approved" || decision.Revision != 2 || task.Status != "ready" || task.Revision != 2 || d.Work.Revision != pending.Work.Revision+1 {
				t.Fatalf("non-atomic approval: before=%+v after=%+v", pending, d)
			}
			return toolReply("file__write", `{"path":"notes.txt","content":"approved\n"}`)(req, key)
		}, textReply("Applied."),
	)
	approvals := 0
	turn, items := workflowTurn(h, th, func(a protocol.Approval) bool {
		if a.Kind == "workflow" {
			approvals++
			assertWorkflowApproval(t, h, th, a, "", "")
			pending = workflowDetail(h, th.ID)
		}
		return true
	})
	if turn.Status != protocol.TurnCompleted || approvals != 1 || len(items) != 4 {
		t.Fatalf("turn=%+v approvals=%d items=%+v", turn, approvals, items)
	}
	if items[0].Status != protocol.ItemDenied || items[0].Tool.Error != "workflow not ready" || items[1].Status != protocol.ItemCompleted || items[3].Status != protocol.ItemCompleted {
		t.Fatalf("gate/discovery/retry=%+v", items)
	}
	if data, err := os.ReadFile(filepath.Join(h.ws, "notes.txt")); err != nil || string(data) != "approved\n" {
		t.Fatalf("retry result=%q %v", data, err)
	}
	var remembered int
	if err := h.eng.Store.DB.QueryRow(`SELECT count(*) FROM thread_approvals WHERE tool='work.update'`).Scan(&remembered); err != nil || remembered != 0 {
		t.Fatalf("workflow approval remembered=%d %v", remembered, err)
	}
}

// A new engine, registry, Store connection and socket client reopen the exact
// same database. Secrets survive via the production memory secret store.
func restartWorkflowHarness(h *harness) {
	h.t.Helper()
	h.c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	h.eng.Shutdown(ctx)
	cancel()
	cfg, secretStore := h.eng.Cfg, h.eng.Secrets
	if err := h.eng.Store.Close(); err != nil {
		h.t.Fatal(err)
	}
	st, err := store.Open(h.ctx, filepath.Join(cfg.Home, "umcode.db"))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { st.Close() })
	fake := &fakeProvider{id: "claude"}
	registry := llm.NewRegistry()
	registry.Register(fake)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := engine.New(h.ctx, engine.Options{Config: cfg, Store: st, Secrets: secretStore, LLMs: registry, Logger: log, DisableScheduler: true})
	if err != nil {
		h.t.Fatal(err)
	}
	srv := server.New(eng, log)
	socket := filepath.Join(filepath.Dir(cfg.Runtime.SocketPath), "restart.sock")
	if err := srv.ListenUnix(socket); err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() {
		srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		eng.Shutdown(ctx)
	})
	c, err := client.Dial(h.ctx, socket, "restart-test", true)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { c.Close() })
	h.eng, h.fake, h.c = eng, fake, c
}

// Catches lost graph identity, orphan approval reuse and approval-free restart
// mutation, while proving reconstruction through the actual compiled request.
func TestDesignedWorkflowApprovalSurvivesRestart(t *testing.T) {
	h, th, _ := workflowFixture(t, "update the public schema", true, true)
	h.fake.push(workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest { return solutionBatch(t, d, true, "") }))
	var started protocol.TurnStartResult
	h.call(protocol.MethodTurnStart, protocol.TurnStartParams{ThreadID: th.ID, Text: "propose"}, &started)
	var old protocol.Approval
	timeout := time.After(10 * time.Second)
	for old.ID == "" {
		select {
		case n := <-h.c.Notifications():
			if n.Method == protocol.NotifyApprovalRequest {
				var ev protocol.ApprovalEvent
				if err := json.Unmarshal(n.Params, &ev); err != nil {
					t.Fatal(err)
				}
				old = ev.Approval
			}
		case <-timeout:
			t.Fatal("proposal did not request approval")
		}
	}
	assertWorkflowApproval(t, h, th, old, "", "")
	// End the proposal transport, then exercise allowed discovery with its
	// persisted gate still proposed. This sends the pending graph to the model
	// before shutdown, so restart can compare actual packet bytes as well as rows.
	h.call(protocol.MethodTurnInterrupt, protocol.TurnInterruptParams{TurnID: started.Turn.ID}, nil)
	h.waitTurn(started.Turn.ID, nil)
	h.fake.push(toolReply("file__list", `{}`), func(llm.Request, string) ([]llm.Event, error) { return nil, context.Canceled })
	inspection, discovery := workflowTurn(h, th, func(a protocol.Approval) bool {
		t.Fatalf("read-only discovery requested approval: %+v", a)
		return false
	})
	if inspection.Status != protocol.TurnInterrupted || len(discovery) != 1 || discovery[0].Status != protocol.ItemCompleted {
		t.Fatalf("pending discovery=%+v items=%+v", inspection, discovery)
	}
	beforePacket := workflowPacket(h, old.NodeID, "designed", "gate=public_contract")
	if beforePacket == "" {
		t.Fatal("persisted pending gate was not compiled before restart")
	}
	before := workflowDetail(h, th.ID)
	restartWorkflowHarness(h)
	after := workflowDetail(h, th.ID)
	// Graceful shutdown records a final workspace fingerprint; semantic state
	// and evidence must retain exact identities and revisions across reopening.
	before.Fingerprints, after.Fingerprints = nil, nil
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("restart changed graph: before=%+v after=%+v", before, after)
	}
	h.fake.push(toolReply("file__write", `{"path":"notes.txt","content":"resumed\n"}`), textReply("Resumed."))
	fresh := 0
	turn, items := workflowTurn(h, th, func(a protocol.Approval) bool {
		if a.Kind == "workflow" {
			fresh++
			assertWorkflowApproval(t, h, th, a, "", "")
			if a.ID == old.ID || a.NodeID != old.NodeID || a.NodeRevision != old.NodeRevision || a.WorkID != old.WorkID {
				t.Fatalf("restart approval=%+v old=%+v", a, old)
			}
		}
		return true
	})
	if turn.Status != protocol.TurnCompleted || fresh != 1 || len(items) != 1 || items[0].Status == protocol.ItemFailed {
		t.Fatalf("restart turn=%+v fresh=%d items=%+v", turn, fresh, items)
	}
	if data, err := os.ReadFile(filepath.Join(h.ws, "notes.txt")); err != nil || string(data) != "resumed\n" {
		t.Fatalf("restart write=%q %v", data, err)
	}
	var approvals protocol.ApprovalListResult
	h.call(protocol.MethodApprovalList, nil, &approvals)
	for _, a := range approvals.Approvals {
		if a.ID == old.ID && a.Status != "expired" {
			t.Fatalf("orphan approval=%+v", a)
		}
	}
	assertWorkflowPacket(t, h, old.NodeID, "designed", "gate=public_contract")
	if got := workflowPacket(h, old.NodeID, "designed", "gate=public_contract"); got != beforePacket {
		t.Fatalf("restart packet drift:\nbefore=%s\nafter=%s", beforePacket, got)
	}
}

func TestRejectedDecisionKeepsTaskBlockedAndAllowsReplacement(t *testing.T) {
	h, th, _ := workflowFixture(t, "update the public schema", true, false)
	h.fake.push(workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest { return solutionBatch(t, d, true, "") }),
		toolReply("file__write", `{"path":"notes.txt","content":"denied"}`), textReply("Waiting."))
	denied := 0
	_, items := workflowTurn(h, th, func(a protocol.Approval) bool {
		if a.Kind == "workflow" {
			denied++
			return false
		}
		return true
	})
	d := workflowDetail(h, th.ID)
	if denied != 1 || workflowNode(t, d, "decision", "").Status != "rejected" || workflowNode(t, d, "task", "").Status != "blocked" || len(items) != 2 || items[1].Tool.Error != "workflow not ready" {
		t.Fatalf("rejection=%+v items=%+v denied=%d", d, items, denied)
	}
	h.fake.push(toolReply("file__write", `{"path":"notes.txt","content":"still denied"}`),
		workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest {
			return solutionBatch(t, d, true, " replacement")
		}),
		func(req llm.Request, key string) ([]llm.Event, error) {
			d := modelWorkflowDetail(t, req)
			var old, replacement protocol.WorkNode
			for _, n := range d.Nodes {
				if n.Kind == "task" {
					if n.Status == "blocked" {
						old = n
					}
					if n.Status == "ready" {
						replacement = n
					}
				}
			}
			raw, _ := json.Marshal(map[string]any{"work_id": d.Work.ID, "expected_revision": d.Work.Revision, "nodes": []map[string]any{{"id": old.ID, "expected_revision": old.Revision, "from_status": old.Status, "to_status": "superseded", "superseded_by": replacement.ID}}})
			return toolReply("work__update", string(raw))(req, key)
		},
		workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest {
			return taskTransition(t, d, "Write notes replacement", "in_progress")
		}),
		toolReply("file__write", `{"path":"notes.txt","content":"replacement\n"}`),
		toolReply("verification__run", `{"checks":[{"label":"go test","command":"go test ./...","reason":"Acceptance contract"},{"label":"go vet","command":"go vet ./...","reason":"Static checks"}]}`),
		workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest {
			return taskTransition(t, d, "Write notes replacement", "completed")
		}), textReply("Applied replacement."))
	approved := 0
	_, items = workflowTurn(h, th, func(a protocol.Approval) bool {
		if a.Kind == "workflow" {
			approved++
			assertWorkflowApproval(t, h, th, a, "Use a small implementation replacement", "Write notes replacement")
		}
		return true
	})
	d = workflowDetail(h, th.ID)
	if approved != 1 || len(items) != 7 || items[0].Tool.Error != "workflow not ready" || items[2].Status != protocol.ItemCompleted || d.Work.Status != "completed" || workflowNode(t, d, "task", "Write notes").Status != "superseded" || workflowNode(t, d, "task", "Write notes replacement").Status != "completed" {
		t.Fatalf("replacement=%+v items=%+v approvals=%d", d, items, approved)
	}
	old, replacement := workflowNode(t, d, "task", "Write notes"), workflowNode(t, d, "task", "Write notes replacement")
	if old.SupersededBy != replacement.ID || old.ValidUntil == nil || old.Revision < 3 {
		t.Fatalf("retirement history missing: %+v", old)
	}
	if data, err := os.ReadFile(filepath.Join(h.ws, "notes.txt")); err != nil || string(data) != "replacement\n" {
		t.Fatalf("replacement write=%q %v", data, err)
	}
}

func TestDesignedWorkflowGeneralPurposeNoProject(t *testing.T) {
	h, th, _ := workflowFixture(t, "design a public contract for a general purpose service", false, false)
	h.fake.push(workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest { return solutionBatch(t, d, true, "") }),
		workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest {
			return taskTransition(t, d, "Write notes", "in_progress")
		}),
		workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest {
			return taskTransition(t, d, "Write notes", "completed")
		}), textReply("Contract approved."))
	approvals := 0
	turn, items := workflowTurn(h, th, func(a protocol.Approval) bool {
		if a.Kind == "workflow" {
			approvals++
			assertWorkflowApproval(t, h, th, a, "", "")
			if a.ProjectID != "" {
				t.Fatalf("project leaked: %+v", a)
			}
		}
		return true
	})
	d := workflowDetail(h, th.ID)
	if turn.Status != protocol.TurnCompleted || d.Work.Status != protocol.WorkCompleted || d.Work.ProjectID != "" || d.Work.WorkflowDepth != "designed" || approvals != 1 || len(items) != 3 || workflowNode(t, d, "task", "").Status != "completed" {
		t.Fatalf("general-purpose=%+v approvals=%d items=%+v", d, approvals, items)
	}
	for _, item := range items {
		if item.Status != protocol.ItemCompleted {
			t.Fatalf("fresh workflow required a failing tool: %+v", item)
		}
	}
	if len(d.Attempts) != 1 || d.Attempts[0].CheckType != "workflow_approval" || d.Attempts[0].Status != "passed" {
		t.Fatalf("acceptance=%+v", d.Attempts)
	}
	for _, ev := range d.Evidence {
		if ev.Kind == protocol.EvidenceFileChange {
			t.Fatalf("file action=%+v", ev)
		}
	}
	for _, name := range []string{"notes.txt", "UMCODE.md", ".umcode"} {
		if _, err := os.Stat(filepath.Join(h.ws, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected project artifact %s: %v", name, err)
		}
	}
	var memories int
	if err := h.eng.Store.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='project_memories'`).Scan(&memories); err != nil || memories != 0 {
		t.Fatalf("curated memory schema=%d %v", memories, err)
	}
}

func TestGuidedWorkflowProjectlessPlanDoesNotImplyApproval(t *testing.T) {
	h, th, _ := workflowFixture(t, "draft a note", false, false)
	h.fake.push(workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest { return solutionBatch(t, d, false, "") }), textReply("Approach recorded."))
	_, items := workflowTurn(h, th, func(a protocol.Approval) bool { t.Fatalf("ungated planning requested approval: %+v", a); return false })
	d := workflowDetail(h, th.ID)
	if len(items) != 1 || items[0].Status != protocol.ItemCompleted || d.Work.WorkflowDepth != "guided" || len(d.Attempts) != 0 || workflowNode(t, d, "criterion", "").Status != "pending" {
		t.Fatalf("planning invented approval or verification: %+v", d)
	}
}

func TestDesignedWorkflowProspectivePathBlocksFirstWrite(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled_%t", enabled), func(t *testing.T) {
			h := newHarness(t, func(c *config.Config) { c.Models.DesignedWorkflow = enabled })
			h.addKey("claude", "workflow", "sk-1")
			th := startWorkThread(h)
			h.fake.push(toolReply("file__write", `{"path":"schema.json","content":"{}"}`), textReply("Stopped."))
			runWorkTurn(h, th, "make the edit")
			_, err := os.Stat(filepath.Join(h.ws, "schema.json"))
			if enabled {
				if !os.IsNotExist(err) {
					t.Fatalf("first risky write executed: %v", err)
				}
				if d := workflowDetail(h, th.ID); d.Work.WorkflowDepth != "designed" || d.Work.Status != "open" {
					t.Fatalf("escalation not persisted: %+v", d.Work)
				}
			} else if err != nil {
				t.Fatalf("flag-off mutation changed: %v", err)
			}
		})
	}
}

func TestGuidedWorkflowMaterialGatePersistsAcrossDenialExpiryAndRestart(t *testing.T) {
	for _, outcome := range []string{"denied", "expired"} {
		t.Run(outcome, func(t *testing.T) {
			h, th, initial := workflowFixture(t, "write notes", true, false)
			if initial.Work.WorkflowDepth != "guided" {
				t.Fatalf("fixture depth=%s", initial.Work.WorkflowDepth)
			}
			if outcome == "expired" {
				h.eng.Cfg.Policy.ApprovalTimeoutMinutes = 0
			}
			h.fake.push(workflowUpdate(h, th, func(d protocol.WorkDetail) protocol.WorkUpdateRequest { return solutionBatch(t, d, true, "") }), textReply("Waiting."))
			if outcome == "expired" {
				runWorkTurn(h, th, "continue")
			} else {
				workflowTurn(h, th, func(protocol.Approval) bool { return false })
			}
			d := workflowDetail(h, th.ID)
			if d.Work.WorkflowDepth != "designed" {
				t.Fatalf("material gate persisted at %s", d.Work.WorkflowDepth)
			}
			restartWorkflowHarness(h)
			h.eng.Cfg.Policy.ApprovalTimeoutMinutes = 0
			h.fake.push(toolReply("file__write", `{"path":"notes.txt","content":"bypass"}`), textReply("Waiting."))
			runWorkTurn(h, th, "continue")
			if _, err := os.Stat(filepath.Join(h.ws, "notes.txt")); !os.IsNotExist(err) {
				t.Fatalf("%s gate bypassed after restart: %v", outcome, err)
			}
			if d := workflowDetail(h, th.ID); d.Work.WorkflowDepth != "designed" {
				t.Fatal("restart lost escalation")
			}
		})
	}
}
