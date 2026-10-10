package engine

import (
	"context"
	"encoding/json"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/toolselect"
	"testing"
)

func TestDiscoveryAdapterSchemaAndCancellation(t *testing.T) {
	c, err := toolselect.NewCatalog([]toolselect.Entry{{CanonicalName: "special.action", WireName: "special__action", Family: "other", Origin: "registry", Spec: llm.ToolSpec{Name: "special__action", Description: "Specialist action", Schema: json.RawMessage(`{"type":"object"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := toolselect.Start(c, toolselect.Signals{})
	if err != nil {
		t.Fatal(err)
	}
	tool := &discoveryTool{state: s}
	var schema map[string]any
	if json.Unmarshal(tool.Schema(), &schema) != nil || schema["additionalProperties"] != false {
		t.Fatal("discovery schema not strict")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := tool.Call(ctx, json.RawMessage(`{"query":"special"}`)); err == nil || len(s.Specs()) != 0 {
		t.Fatal("cancelled discovery changed state")
	}
	if _, err := tool.Call(t.Context(), json.RawMessage(`{"names":["special.action"]}`)); err != nil || len(s.Specs()) != 1 {
		t.Fatal("discovery did not load schema", err)
	}
}
