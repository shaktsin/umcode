package engine

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/tools"
	"github.com/shaktsin/umcode/internal/toolselect"
)

const discoveryInstruction = "Core tools are available. Use tools.discover to find an omitted capability; loaded schemas appear on the next request."

type discoveryTool struct {
	state  *toolselect.State
	before func(context.Context) error
}

func (*discoveryTool) Name() string { return "tools.discover" }
func (*discoveryTool) Description() string {
	return "Find permitted tools by query or exact canonical/wire names and load their schemas on the next provider request. Does not execute tools."
}
func (*discoveryTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","maxLength":512},"names":{"type":"array","maxItems":8,"minItems":1,"items":{"type":"string","minLength":1,"maxLength":4096}},"cursor":{"type":"string","maxLength":2048}},"anyOf":[{"required":["query"]},{"required":["names"]}],"additionalProperties":false}`)
}
func (*discoveryTool) Assess(json.RawMessage) (tools.Risk, string) {
	return tools.RiskGreen, "Load tool schemas for the next request"
}
func (t *discoveryTool) Call(ctx context.Context, args json.RawMessage) (output string, err error) {
	defer func() {
		if recover() != nil {
			if t.state != nil {
				t.state.Fallback("discovery_error")
			}
			output = ""
			err = errors.New("discovery unavailable")
		}
	}()
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if t.state == nil {
		return "", errors.New("discovery unavailable")
	}
	if t.before != nil {
		if err = t.before(ctx); err != nil {
			t.state.Fallback("discovery_error")
			return "", errors.New("discovery unavailable")
		}
	}
	r, err := t.state.Discover(args)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.state.Fallback("discovery_error")
		return "", errors.New("discovery unavailable")
	}
	return string(b), nil
}
