package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/agentrt"
	"github.com/shaktsin/umcode/internal/protocol"
)

type fakeRT struct {
	spawn     agentrt.SpawnRequest
	spawnRes  agentrt.SpawnResult
	spawnErr  error
	waitIDs   []string
	waitMode  agentrt.WaitMode
	waitCalls int
	msgTask   string
	msgFrom   string
	msgTo     string
	msgBody   string
	cancelID  string
}

func (f *fakeRT) Spawn(_ context.Context, r agentrt.SpawnRequest) (agentrt.SpawnResult, error) {
	f.spawn = r
	return f.spawnRes, f.spawnErr
}
func (f *fakeRT) List(context.Context, string) ([]protocol.RuntimeAgent, error) {
	return []protocol.RuntimeAgent{{ID: "a1"}}, nil
}
func (f *fakeRT) Status(_ context.Context, _, id string) (protocol.RuntimeAgent, error) {
	return protocol.RuntimeAgent{ID: id}, nil
}
func (f *fakeRT) Wait(_ context.Context, _ string, ids []string, m agentrt.WaitMode) ([]protocol.RuntimeAgent, error) {
	f.waitCalls++
	f.waitIDs, f.waitMode = ids, m
	return []protocol.RuntimeAgent{{ID: ids[0]}}, nil
}
func (f *fakeRT) Message(_ context.Context, task, from, to, body string) (protocol.AgentMessage, error) {
	f.msgTask, f.msgFrom, f.msgTo, f.msgBody = task, from, to, body
	return protocol.AgentMessage{ID: "m1", ToAgentID: to}, nil
}
func (f *fakeRT) Cancel(_ context.Context, _, id string) error { f.cancelID = id; return nil }

func rtTools(f *fakeRT) map[string]Tool {
	auth := func(context.Context) (protocol.AgentTask, error) { return protocol.AgentTask{ID: "task1"}, nil }
	m := map[string]Tool{}
	for _, t := range NewRuntimeAgentTools(f, auth, func(context.Context) string { return "caller1" }) {
		m[t.Name()] = t
	}
	return m
}

func TestSpawnToolRequiresStableCallID(t *testing.T) {
	_, err := rtTools(&fakeRT{})["agent.spawn"].Call(context.Background(), json.RawMessage(`{"planEntry":"p","assignment":{"prompt":"x"}}`))
	if err == nil || !strings.Contains(err.Error(), "stable tool call ID required") {
		t.Fatalf("err=%v", err)
	}
}

func TestSpawnToolRejectsUnknownFields(t *testing.T) {
	ctx := WithAgentCallID(context.Background(), "c1")
	_, err := rtTools(&fakeRT{})["agent.spawn"].Call(ctx, json.RawMessage(`{"planEntry":"p","role":"reviewer","assignment":{"prompt":"x"}}`))
	if err == nil || !strings.Contains(err.Error(), "role") {
		t.Fatalf("err=%v", err)
	}
}

func TestSpawnToolPassesRequestAndCaller(t *testing.T) {
	f := &fakeRT{spawnRes: agentrt.SpawnResult{
		Agent:        protocol.RuntimeAgent{ID: "a9", Name: "w", State: protocol.AgentStateQueued, ModelRequested: "m1", ModelActual: "m2"},
		Granted:      protocol.AgentCapabilities{Files: protocol.FilesRead, Shell: protocol.ShellNone, SpawnMax: 1},
		Rejected:     []protocol.CapabilityRejection{{Field: "capabilities.network", Reason: "no"}},
		QueuedReason: "capacity",
	}}
	ctx := WithAgentCallID(context.Background(), "c1")
	out, err := rtTools(f)["agent.spawn"].Call(ctx, json.RawMessage(`{"name":"n","planEntry":"p","assignment":{"prompt":"do","acceptanceChecks":["a"],"contextRefs":["r"]},"capabilities":{"files":"read","shell":"root","network":true,"spawnMax":2},"model":"m1","context":"summary","budget":{"share":0.5}}`))
	if err != nil {
		t.Fatal(err)
	}
	r := f.spawn
	if r.TaskID != "task1" || r.CallerAgentID != "caller1" || r.CallID != "c1" || r.PlanEntry != "p" || r.Name != "n" ||
		r.Model != "m1" || r.ContextMode != "summary" || r.BudgetShare != 0.5 || r.Assignment.Prompt != "do" ||
		len(r.Assignment.AcceptanceChecks) != 1 || len(r.Assignment.ContextRefs) != 1 {
		t.Fatalf("req=%+v", r)
	}
	if r.Capabilities == nil || r.Capabilities.Files != "read" || r.Capabilities.Shell != "root" || !r.Capabilities.Network || r.Capabilities.SpawnMax != 2 {
		t.Fatalf("caps=%+v", r.Capabilities)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res["id"] != "a9" || res["queuedReason"] != "capacity" || res["granted"] == nil || len(res["rejected"].([]any)) != 1 {
		t.Fatalf("res=%v", res)
	}
	if res["model"].(map[string]any)["actual"] != "m2" || res["budget"].(map[string]any)["share"] == nil {
		t.Fatalf("res=%v", res)
	}
	// capabilities absent -> nil
	f2 := &fakeRT{}
	out, err = rtTools(f2)["agent.spawn"].Call(ctx, json.RawMessage(`{"planEntry":"p","assignment":{"prompt":"x"}}`))
	if err != nil || f2.spawn.Capabilities != nil {
		t.Fatalf("err=%v caps=%v", err, f2.spawn.Capabilities)
	}
	if !strings.Contains(out, `"rejected":[]`) {
		t.Fatalf("rejected must be [] : %s", out)
	}
}

func TestSpawnToolSurfacesAdmissionError(t *testing.T) {
	f := &fakeRT{spawnErr: &agentrt.AdmissionError{Field: "planEntry", Message: "plan has no agent named \"p\""}}
	ctx := WithAgentCallID(context.Background(), "c1")
	_, err := rtTools(f)["agent.spawn"].Call(ctx, json.RawMessage(`{"planEntry":"p","assignment":{"prompt":"x"}}`))
	if err == nil || !strings.Contains(err.Error(), "planEntry") || !strings.Contains(err.Error(), "plan has no agent") {
		t.Fatalf("err=%v", err)
	}
	f.spawnErr = errors.New("boom")
	_, err = rtTools(f)["agent.spawn"].Call(ctx, json.RawMessage(`{"planEntry":"p","assignment":{"prompt":"x"}}`))
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err=%v", err)
	}
}

func TestWaitToolDefaultsToAny(t *testing.T) {
	f := &fakeRT{}
	if _, err := rtTools(f)["agent.wait"].Call(context.Background(), json.RawMessage(`{"ids":["a"]}`)); err != nil {
		t.Fatal(err)
	}
	if f.waitMode != agentrt.WaitAny {
		t.Fatalf("mode=%v", f.waitMode)
	}
	if _, err := rtTools(f)["agent.wait"].Call(context.Background(), json.RawMessage(`{"ids":["a"],"mode":"all"}`)); err != nil || f.waitMode != agentrt.WaitAll {
		t.Fatalf("err=%v mode=%v", err, f.waitMode)
	}
	if _, err := rtTools(f)["agent.wait"].Call(context.Background(), json.RawMessage(`{"ids":["a"],"mode":"some"}`)); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestWaitToolRejectsMoreThan32IDs(t *testing.T) {
	f := &fakeRT{}
	ids := make([]string, 33)
	for i := range ids {
		ids[i] = "x"
	}
	b, _ := json.Marshal(map[string]any{"ids": ids})
	if _, err := rtTools(f)["agent.wait"].Call(context.Background(), b); err == nil || f.waitCalls != 0 {
		t.Fatalf("err=%v calls=%d", err, f.waitCalls)
	}
}

func TestToolNamesAreExactlyTheRuntimeSet(t *testing.T) {
	got := rtTools(&fakeRT{})
	want := []string{"agent.spawn", "agent.list", "agent.status", "agent.wait", "agent.message", "agent.cancel"}
	if len(got) != len(want) {
		t.Fatalf("got %d tools", len(got))
	}
	for _, n := range want {
		tl, ok := got[n]
		if !ok {
			t.Fatalf("missing %s", n)
		}
		if r, _ := tl.Assess(nil); r != RiskGreen {
			t.Fatalf("%s risk", n)
		}
		if a, b := tl.(interface{ SelectionMetadata() (string, string) }).SelectionMetadata(); a != "core" || b != "engine" {
			t.Fatalf("%s meta", n)
		}
	}
	if _, ok := got["agent.followup"]; ok {
		t.Fatal("followup present")
	}
}

func TestRuntimeAgentListStatusMessageCancel(t *testing.T) {
	f := &fakeRT{}
	m := rtTools(f)
	ctx := context.Background()
	if out, err := m["agent.list"].Call(ctx, json.RawMessage(`{}`)); err != nil || !strings.Contains(out, "a1") {
		t.Fatalf("%v %s", err, out)
	}
	want, _ := json.Marshal([]protocol.RuntimeAgent{{ID: "a1"}})
	for _, in := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`{}`)} {
		out, err := m["agent.list"].Call(ctx, in)
		if err != nil || out != string(want) {
			t.Fatalf("args=%q err=%v out=%s", in, err, out)
		}
	}
	if _, err := m["agent.list"].Call(ctx, json.RawMessage(`{} {}`)); err == nil {
		t.Fatal("list accepted trailing data")
	}
	if _, err := m["agent.list"].Call(ctx, json.RawMessage(`{"x":1}`)); err == nil {
		t.Fatal("list accepted fields")
	}
	if out, err := m["agent.status"].Call(ctx, json.RawMessage(`{"id":"a2"}`)); err != nil || !strings.Contains(out, "a2") {
		t.Fatalf("%v %s", err, out)
	}
	if _, err := m["agent.message"].Call(ctx, json.RawMessage(`{"to":"a2","message":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	if f.msgTask != "task1" || f.msgFrom != "caller1" || f.msgTo != "a2" || f.msgBody != "hi" {
		t.Fatalf("%+v", f)
	}
	if _, err := m["agent.cancel"].Call(ctx, json.RawMessage(`{"id":"a3"}`)); err != nil || f.cancelID != "a3" {
		t.Fatalf("%v %s", err, f.cancelID)
	}
}

func TestSpawnToolRejectsIncompleteOrInvalidInputs(t *testing.T) {
	ctx := WithAgentCallID(context.Background(), "c1")
	base := `{"planEntry":"p","assignment":{"prompt":"x"},`
	cases := map[string]string{
		`"capabilities":{"network":true}`:                                              "capabilities.files",
		`"capabilities":{"files":"read","network":true,"spawnMax":0}`:                  "capabilities.shell",
		`"capabilities":{"files":"read","shell":"none","spawnMax":0}`:                  "capabilities.network",
		`"capabilities":{"files":"read","shell":"none","network":false}`:               "capabilities.spawnMax",
		`"capabilities":{"files":"read","shell":"none","network":false,"spawnMax":-1}`: "spawnMax",
		`"budget":{"share":1.5}`:                                                       "budget.share",
		`"budget":{"share":-0.1}`:                                                      "budget.share",
	}
	for body, want := range cases {
		f := &fakeRT{}
		_, err := rtTools(f)["agent.spawn"].Call(ctx, json.RawMessage(base+body+`}`))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err=%v", body, err)
		}
		if f.spawn.CallID != "" {
			t.Fatalf("%s: service called", body)
		}
	}
	f := &fakeRT{}
	_, err := rtTools(f)["agent.spawn"].Call(ctx, json.RawMessage(base+`"capabilities":{"files":"root","shell":"root","network":false,"spawnMax":0}}`))
	if err != nil || f.spawn.Capabilities == nil || f.spawn.Capabilities.Files != "root" || f.spawn.Capabilities.Shell != "root" {
		t.Fatalf("err=%v caps=%+v", err, f.spawn.Capabilities)
	}
}
