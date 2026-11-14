package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAssistantBuildConnectionRespectsEditorRules(t *testing.T) {
	graph := `{"version":1,"format":"umcode.layers.v1","nodes":[{"id":"input","kind":"input","config":{}},{"id":"middle","kind":"linear","config":{}},{"id":"output","kind":"output","config":{}}],"edges":[{"id":"a","source":"input","target":"middle"},{"id":"b","source":"middle","target":"output"}]}`
	for _, tc := range []struct{ name, op string }{
		{"second outgoing", `{"type":"connect","source":"input","target":"output"}`},
		{"second incoming", `{"type":"connect","source":"output","target":"middle"}`},
		{"loop", `{"type":"connect","source":"output","target":"input"}`},
		{"remove input", `{"type":"remove","nodeId":"input"}`},
		{"remove output", `{"type":"remove","nodeId":"output"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := applyBuildOperations(graph, []json.RawMessage{json.RawMessage(tc.op)})
			if err == nil {
				t.Fatal("invalid change was accepted")
			}
		})
	}
	updated, err := applyBuildOperations(graph, []json.RawMessage{json.RawMessage(`{"type":"disconnect","edgeId":"b"}`), json.RawMessage(`{"type":"connect","source":"input","target":"output"}`)})
	if err == nil || !strings.Contains(err.Error(), "already connected") {
		t.Fatalf("expected input output limit, got %s, %v", updated, err)
	}
}
