package server_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
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
