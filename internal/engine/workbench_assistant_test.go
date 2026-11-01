package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func assistantSnapshot() protocol.WorkbenchTurnContext {
	graph := `{"version":1,"format":"umcode.layers.v1","nodes":[{"id":"input","kind":"input","position":{"x":0,"y":0},"config":{"mode":"features","features":16}},{"id":"dense","kind":"linear","position":{"x":0,"y":130},"config":{"outFeatures":3,"bias":true,"tieEmbedding":false}}],"edges":[{"id":"edge","source":"input","target":"dense"}]}`
	return protocol.WorkbenchTurnContext{ProjectID: "p", ModelID: "m", ModelName: "Tiny", GraphJSON: graph, Revision: fmt.Sprintf("%x", sha256.Sum256([]byte(graph))), SelectedNodeID: "dense", Summary: `{"parameters":51}`}
}

func TestWorkbenchAssistantSnapshot(t *testing.T) {
	good := assistantSnapshot()
	if err := validateWorkbenchSnapshot(&good); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*protocol.WorkbenchTurnContext){
		"revision":  func(c *protocol.WorkbenchTurnContext) { c.Revision = strings.Repeat("0", 64) },
		"selection": func(c *protocol.WorkbenchTurnContext) { c.SelectedNodeID = "missing" },
		"oversized": func(c *protocol.WorkbenchTurnContext) { c.GraphJSON = strings.Repeat(" ", 1048577) },
		"summary":   func(c *protocol.WorkbenchTurnContext) { c.Summary = strings.Repeat("x", 16385) },
		"format": func(c *protocol.WorkbenchTurnContext) {
			c.GraphJSON = `{"version":1,"format":"custom","nodes":[],"edges":[]}`
			c.Revision = fmt.Sprintf("%x", sha256.Sum256([]byte(c.GraphJSON)))
		},
		"duplicate": func(c *protocol.WorkbenchTurnContext) {
			c.GraphJSON = strings.Replace(c.GraphJSON, `"id":"dense"`, `"id":"input"`, 1)
			c.Revision = fmt.Sprintf("%x", sha256.Sum256([]byte(c.GraphJSON)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := assistantSnapshot()
			mutate(&c)
			if validateWorkbenchSnapshot(&c) == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
}

func TestWorkbenchAssistantProposalScopeAndBounds(t *testing.T) {
	tool := &workbenchProposalTool{}
	args := json.RawMessage(`{"explanation":"Increase width","operations":[{"type":"update","nodeId":"dense","config":{"outFeatures":8}}]}`)
	if _, err := tool.Call(context.Background(), args); err == nil {
		t.Fatal("proposal outside an admitted turn")
	}
	c := assistantSnapshot()
	ctx := withWorkbenchContext(context.WithValue(context.Background(), executionTurnKey{}, protocol.Turn{ID: "t", ThreadID: "th"}), &c)
	out, err := tool.Call(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Proposal protocol.WorkbenchProposal `json:"workbenchProposal"`
	}
	if json.Unmarshal([]byte(out), &result) != nil || result.Proposal.ProjectID != "p" || result.Proposal.ModelID != "m" || result.Proposal.Revision != c.Revision {
		t.Fatalf("unbound proposal: %s", out)
	}
	for _, raw := range []string{
		`{"explanation":"x","operations":[]}`,
		`{"explanation":"x","operations":[{"type":"update","nodeId":"dense","config":{},"path":"model.py"}]}`,
		`{"explanation":"x","projectId":"other","operations":[{"type":"remove","nodeId":"dense"}]}`,
		`{"explanation":"x","operations":[{"type":"add","ref":"x","kind":"python"}]}`,
		`{"explanation":"x","operations":[{"type":"run","command":"touch escaped"}]}`,
		`{"explanation":"x","operations":[{"type":"update","nodeId":"dense","config":{"x":null}}]}`,
		`{"explanation":"x","operations":[{"type":"remove","nodeId":"dense"}]} {}`,
	} {
		if _, err := tool.Call(ctx, json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid proposal accepted: %s", raw)
		}
	}
	tooMany := `{"explanation":"x","operations":[` + strings.TrimSuffix(strings.Repeat(`{"type":"remove","nodeId":"dense"},`, 33), ",") + `]}`
	if _, err := tool.Call(ctx, json.RawMessage(tooMany)); err == nil {
		t.Fatal("33 operations accepted")
	}
	if _, err := tool.Call(ctx, json.RawMessage(strings.Repeat(" ", 65537))); err == nil {
		t.Fatal("oversized proposal accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := tool.Call(cancelled, args); err == nil {
		t.Fatal("cancelled call returned actionable data")
	}
}
