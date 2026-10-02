package engine

import "github.com/shaktsin/umcode/internal/llm"

// RequestBreakdown estimates where one model request's input tokens go. It is
// diagnostic only: nothing in it changes what is sent to the model.
type RequestBreakdown struct {
	Layers             map[string]int `json:"layers"`             // tokens per system-prompt layer
	SystemTokens       int            `json:"systemTokens"`       // sum of Layers
	ToolSpecTokens     int            `json:"toolSpecTokens"`     // tool names, descriptions and schemas
	ToolCount          int            `json:"toolCount"`          // tools exposed on the request
	ConversationTokens int            `json:"conversationTokens"` // messages excluding tool results
	ToolResultTokens   int            `json:"toolResultTokens"`   // tool result bodies
	TotalTokens        int            `json:"totalTokens"`
}

// measureRequest estimates the token cost of a request by layer, using the same
// estimator as context trimming. layers are the parts of req.System.
func measureRequest(layers []promptLayer, req llm.Request) RequestBreakdown {
	b := RequestBreakdown{Layers: map[string]int{}, ToolCount: len(req.Tools)}
	for _, l := range layers {
		n := tokens(l.Text)
		b.Layers[l.Name] += n
		b.SystemTokens += n
	}
	b.ToolSpecTokens = toolSpecTokens(req.Tools)
	for _, m := range req.Messages {
		if m.Role == llm.RoleTool {
			b.ToolResultTokens += tokens(m.Result)
		}
	}
	b.ConversationTokens = estimateMessageTokens(req.Messages) - b.ToolResultTokens
	b.TotalTokens = b.SystemTokens + b.ToolSpecTokens + b.ConversationTokens + b.ToolResultTokens
	return b
}
