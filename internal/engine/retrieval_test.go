package engine

import (
	"context"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/retrieval"
	"time"

	"reflect"
	"strings"
	"testing"
)

func TestRetrievalEngineOlderContext(t *testing.T) {
	e, th, turn, st := compilerEngine(t, true)
	items, err := st.ListItems(t.Context(), th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	items[0].Text = "QUASAR parser preserves empty bytes"
	if err := st.SaveItem(t.Context(), items[0]); err != nil {
		t.Fatal(err)
	}
	e.Cfg.Models.ContextRetrieval = true
	d := openWorkDetail(t, st, th.ID)
	cs, report, rerr := e.retrieve(t.Context(), retrieval.Scope{ThreadID: th.ID, WorkID: d.Work.ID, ProjectID: th.ProjectID, TurnID: turn.ID}, d, "QUASAR parser", "", items, turn.ID)
	if rerr != nil || len(cs) == 0 {
		t.Fatalf("candidates=%d report=%+v err=%v", len(cs), report, rerr)
	}
	got, ok := e.compile(t.Context(), th, turn.ID, 200000, 100000, items, "QUASAR parser", "")
	if !ok || !strings.Contains(joined(got.msgs), "QUASAR parser preserves empty bytes") || got.packets.Retrieval <= 0 {
		t.Fatalf("retrieval missing: %+v %s", got.packets, joined(got.msgs))
	}
}

func TestRetrievalFallbackAtomic(t *testing.T) {
	e, th, turn, st := compilerEngine(t, true)
	items, _ := st.ListItems(t.Context(), th.ID, 0)
	base, bok := e.compile(t.Context(), th, turn.ID, 200000, 100000, items, "parser", "")
	e.Cfg.Models.ContextRetrieval = true
	t.Cleanup(func() { retrievalHook = nil })
	for _, hook := range []func(context.Context){func(context.Context) { panic("private source must not be logged") }, func(ctx context.Context) { <-ctx.Done() }} {
		called := false
		retrievalHook = func(ctx context.Context) { called = true; hook(ctx) }
		got, ok := e.compile(t.Context(), th, turn.ID, 200000, 100000, items, "parser", "")
		if !called {
			t.Fatal("retrieval failure seam not reached")
		}
		if ok != bok || !reflect.DeepEqual(got.msgs, base.msgs) {
			t.Fatal("retrieval failure changed base")
		}
	}
	retrievalHook = nil
	t.Cleanup(func() { retrievalHook = nil })
}

func TestRetrievalAccounting(t *testing.T) {
	req := llm.Request{Messages: []llm.Message{llm.Text(llm.RoleUser, strings.Repeat("context ", 200))}}
	before := measureRequest(nil, req, RequestPackets{}, nil)
	got := measureRequest(nil, req, RequestPackets{Work: 10, Evidence: 20, Retrieval: 30}, nil)
	if got.TotalTokens != before.TotalTokens || got.ConversationTokens != before.ConversationTokens-60 || got.RetrievalPacketTokens != 30 {
		t.Fatal(got)
	}
}

func TestRetrievalFlagOffEquivalent(t *testing.T) {
	e, th, turn, st := compilerEngine(t, true)
	items, err := st.ListItems(t.Context(), th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	base, ok := e.compile(t.Context(), th, turn.ID, 200000, 100000, items, "parser", "")
	if !ok {
		t.Fatal("baseline declined")
	}
	called := false
	retrievalHook = func(context.Context) { called = true }
	t.Cleanup(func() { retrievalHook = nil })
	got, ok := e.compile(t.Context(), th, turn.ID, 200000, 100000, items, "private unrelated query", t.TempDir())
	if !ok || called || !reflect.DeepEqual(base, got) {
		t.Fatal("flag off changed request or invoked retrieval")
	}
}

func TestRetrievalStoreFailureKeepsBase(t *testing.T) {
	e, th, turn, st := compilerEngine(t, true)
	items, err := st.ListItems(t.Context(), th.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	base, bok := e.compile(t.Context(), th, turn.ID, 200000, 100000, items, "parser", "")
	if _, err := st.DB.Exec(`ALTER TABLE retrieval_documents RENAME TO unavailable_retrieval_documents`); err != nil {
		t.Fatal(err)
	}
	e.Cfg.Models.ContextRetrieval = true
	got, ok := e.compile(t.Context(), th, turn.ID, 200000, 100000, items, "parser", "")
	if ok != bok || !reflect.DeepEqual(got.msgs, base.msgs) {
		t.Fatal("store failure changed base request")
	}
}

func TestRetrievalLexicalCannotRestoreStalePass(t *testing.T) {
	e, th, turn, p, id, _ := memoryEngine(t)
	if _, err := e.Store.AddEvidence(t.Context(), protocol.Evidence{WorkID: id, Kind: protocol.EvidenceFileChange, SourceURI: "main.go", ObservedAt: time.Now().Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	d, err := e.Store.GetWorkDetail(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	cs, _, err := e.retrieve(t.Context(), retrieval.Scope{ThreadID: th.ID, ProjectID: th.ProjectID, WorkID: id, TurnID: turn.ID}, d, "PRIVATE EVIDENCE", p.Root, nil, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Kind == "evidence" && strings.Contains(c.Body, "PRIVATE EVIDENCE BODY") {
			t.Fatal("lexical hit resurrected stale criterion evidence")
		}
	}
}
