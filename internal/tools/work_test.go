package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"github.com/shaktsin/umcode/internal/work"
)

type workUpdateSpy struct {
	calls  int
	thread string
	req    protocol.WorkUpdateRequest
	result protocol.WorkUpdateResult
	gates  []protocol.WorkflowGate
	err    error
}

func (s *workUpdateSpy) Update(_ context.Context, thread string, req protocol.WorkUpdateRequest) (protocol.WorkUpdateResult, []protocol.WorkflowGate, error) {
	s.calls++
	s.thread, s.req = thread, req
	return s.result, s.gates, s.err
}

func TestWorkUpdateSchema(t *testing.T) {
	tool := NewWorkUpdate(&workUpdateSpy{})
	if tool.Name() != "work.update" {
		t.Fatal(tool.Name())
	}
	var root map[string]any
	if err := json.Unmarshal(tool.Schema(), &root); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(root["required"], []any{"work_id", "expected_revision"}) {
		t.Fatal(root["required"])
	}
	if root["additionalProperties"] != false {
		t.Fatal("unexpected top-level operations allowed")
	}
	props := root["properties"].(map[string]any)
	if len(props) != 6 {
		t.Fatalf("unexpected operations: %v", props)
	}
	checks := []struct {
		path []string
		key  string
		want float64
	}{
		{[]string{"work_id"}, "maxLength", 128},
		{[]string{"expected_revision"}, "minimum", 1},
		{[]string{"rationale"}, "maxLength", work.MaxRationaleBytes},
		{[]string{"nodes"}, "maxItems", work.MaxNodeChanges},
		{[]string{"edges"}, "maxItems", work.MaxEdgeChanges},
		{[]string{"nodes", "items", "properties", "ref"}, "maxLength", work.MaxClientRefBytes},
		{[]string{"nodes", "items", "properties", "id"}, "maxLength", 128},
		{[]string{"nodes", "items", "properties", "title"}, "maxLength", work.MaxNodeTitleBytes},
		{[]string{"nodes", "items", "properties", "evidence_ids"}, "maxItems", work.MaxNodeEvidenceIDs},
		{[]string{"nodes", "items", "properties", "evidence_ids", "items"}, "maxLength", 128},
		{[]string{"edges", "items", "properties", "from"}, "maxLength", 128},
		{[]string{"edges", "items", "properties", "to"}, "maxLength", 128},
	}
	for _, check := range checks {
		var value any = props
		for _, key := range check.path {
			value = value.(map[string]any)[key]
		}
		if got := value.(map[string]any)[check.key]; got != check.want {
			t.Errorf("%v.%s = %v, want %v", check.path, check.key, got, check.want)
		}
	}
	for _, bound := range []int{work.MaxNodeContentBytes, work.MaxWorkUpdateBytes} {
		if !strings.Contains(string(tool.Schema()), fmt.Sprint(bound)) {
			t.Fatal("missing content/request byte bounds")
		}
	}
	for _, forbidden := range []string{"promoted", "file.write", "memory.promote", "promotion"} {
		if strings.Contains(string(tool.Schema()), forbidden) {
			t.Fatalf("exposed %s", forbidden)
		}
	}
	kinds := props["nodes"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["kind"].(map[string]any)["enum"]
	if !reflect.DeepEqual(kinds, []any{"requirement", "non_goal", "option", "decision", "task", "unknown", "memory_candidate"}) {
		t.Fatalf("schema exposes unsupported creation kinds: %v", kinds)
	}
}

func TestWorkUpdateSchemaMemoryCandidate(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(NewWorkUpdate(&workUpdateSpy{}).Schema(), &root); err != nil {
		t.Fatal(err)
	}
	content := root["properties"].(map[string]any)["nodes"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["content"].(map[string]any)
	props, ok := content["properties"].(map[string]any)
	if !ok {
		t.Fatal("candidate fields are not advertised")
	}
	for _, field := range []string{"category", "semantic_key", "text", "scope_paths", "source_revision", "replaces_memory"} {
		if _, ok := props[field]; !ok {
			t.Errorf("missing %s", field)
		}
	}
	if _, ok := props["scope"]; ok {
		t.Fatal("legacy scope advertised")
	}
	for field, bound := range map[string]float64{"semantic_key": 128, "text": 512} {
		if props[field].(map[string]any)["maxLength"] != bound {
			t.Errorf("incorrect %s bound", field)
		}
	}
	paths := props["scope_paths"].(map[string]any)
	if paths["maxItems"] != float64(16) || paths["items"].(map[string]any)["maxLength"] != float64(512) {
		t.Fatal("incorrect scope bounds")
	}
}

func TestWorkUpdateRequiresThreadScope(t *testing.T) {
	for _, ctx := range []context.Context{t.Context(), WithScope(t.Context(), &Scope{}), WithScope(t.Context(), &Scope{ThreadID: " "})} {
		for _, apply := range []bool{false, true} {
			spy := &workUpdateSpy{}
			tool := NewWorkUpdate(spy)
			var err error
			if apply {
				_, _, err = tool.Apply(ctx, json.RawMessage(`{"work_id":"w","expected_revision":1}`))
			} else {
				_, err = tool.Call(ctx, json.RawMessage(`{"work_id":"w","expected_revision":1}`))
			}
			if err == nil || spy.calls != 0 {
				t.Fatalf("scope bypass: error=%v calls=%d", err, spy.calls)
			}
		}
	}
}

func TestWorkUpdateApplyAndCompactCall(t *testing.T) {
	args := json.RawMessage(`{"work_id":"w","expected_revision":4,"workflow_depth":"designed","nodes":[{"ref":"task","kind":"task","title":"PRIVATE NODE","content":{"required":true},"evidence_ids":["ev_1"]}],"edges":[{"from":"task","relation":"serves","to":"goal"}],"rationale":"PRIVATE RATIONALE"}`)
	want := protocol.WorkUpdateResult{Revision: 5, Created: 1, Transitioned: 2, Linked: 3}
	gates := []protocol.WorkflowGate{{WorkID: "w", NodeID: "n", Kind: "security", Reason: "PRIVATE GATE", Summary: "PRIVATE SUMMARY"}}
	for _, apply := range []bool{false, true} {
		spy := &workUpdateSpy{result: want, gates: gates}
		tool := NewWorkUpdate(spy)
		var _ WorkflowUpdateTool = tool
		ctx := WithScope(t.Context(), &Scope{ThreadID: "thread-1"})
		if apply {
			got, returned, err := tool.Apply(ctx, args)
			if err != nil || got != want || !reflect.DeepEqual(returned, gates) {
				t.Fatalf("Apply=%v %v %v", got, returned, err)
			}
		} else {
			got, err := tool.Call(ctx, args)
			if err != nil || got != `{"revision":5,"created":1,"transitioned":2,"linked":3}` {
				t.Fatalf("Call=%q %v", got, err)
			}
		}
		if spy.calls != 1 || spy.thread != "thread-1" || spy.req.ExpectedRevision != 4 || spy.req.Nodes[0].Title != "PRIVATE NODE" || spy.req.Rationale != "PRIVATE RATIONALE" || len(spy.req.Edges) != 1 {
			t.Fatalf("wrong forwarding: %+v", spy)
		}
	}
}

func TestWorkUpdateErrorsAreCompact(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		terms []string
	}{
		{"validation", &work.ValidationError{Field: "nodes.to_status", Code: "transition"}, []string{"work.update rejected:", "nodes.to_status", "transition", "correct"}},
		{"stale", &work.ValidationError{Field: "expected_revision", Code: "stale"}, []string{"work.update rejected:", "revision", "stale", "refresh"}},
		{"conflict", store.ErrWorkUpdateConflict, []string{"work.update rejected:", "conflict", "refresh"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &workUpdateSpy{err: tc.err}
			_, _, err := NewWorkUpdate(spy).Apply(WithScope(t.Context(), &Scope{ThreadID: "t"}), json.RawMessage(`{"work_id":"w","expected_revision":1}`))
			if err == nil {
				t.Fatal("missing error")
			}
			for _, term := range tc.terms {
				if !strings.Contains(err.Error(), term) {
					t.Fatalf("missing %q: %v", term, err)
				}
			}
			if !errors.Is(err, tc.err) || len(err.Error()) > 200 {
				t.Fatalf("lost compact typed cause: %v", err)
			}
		})
	}
}

func TestWorkUpdateRejectsMalformedAndOversizedInput(t *testing.T) {
	for _, args := range []json.RawMessage{json.RawMessage(`{"work_id":`), json.RawMessage(`{"work_id":"w","expected_revision":1,"promotion":true}`), json.RawMessage(`{"work_id":"w","expected_revision":1,"rationale":"` + strings.Repeat("x", 65536) + `"}`)} {
		spy := &workUpdateSpy{}
		_, _, err := NewWorkUpdate(spy).Apply(WithScope(t.Context(), &Scope{ThreadID: "t"}), args)
		if err == nil || spy.calls != 0 || len(err.Error()) > 200 {
			t.Fatalf("error=%v calls=%d", err, spy.calls)
		}
	}
}

func TestWorkUpdateAssessGreen(t *testing.T) {
	risk, summary := NewWorkUpdate(&workUpdateSpy{}).Assess(json.RawMessage(`{"rationale":"PRIVATE"}`))
	if risk != RiskGreen || !strings.Contains(summary, "graph") || strings.Contains(summary, "PRIVATE") {
		t.Fatalf("%s %q", risk, summary)
	}
}
