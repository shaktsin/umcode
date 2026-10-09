package engine

import (
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/toolreduce"
	"github.com/shaktsin/umcode/internal/tools"
)

// A provider may reuse a call ID in later model requests in the same turn.
// The ordinal identifies the exact tool message that received a reduction.
type toolReductionKey struct {
	CallID     string
	Occurrence int
}

type ToolReductions map[toolReductionKey]toolreduce.Report

func nextToolReductionKey(occurrences map[string]int, callID string) toolReductionKey {
	occurrences[callID]++
	return toolReductionKey{CallID: callID, Occurrence: occurrences[callID]}
}

// RequestBreakdown estimates where one model request's input tokens go. It is
// diagnostic only: nothing in it changes what is sent to the model.
type RequestPackets struct {
	Work     int `json:"work"`
	Evidence int `json:"evidence"`
	P0       int `json:"p0,omitempty"`
	P1       int `json:"p1,omitempty"`
}

type RequestBreakdown struct {
	Layers                 map[string]int `json:"layers"`         // tokens per system-prompt layer
	SystemTokens           int            `json:"systemTokens"`   // sum of Layers
	ToolSpecTokens         int            `json:"toolSpecTokens"` // tool names, descriptions and schemas
	ToolCount              int            `json:"toolCount"`      // tools exposed on the request
	WorkUpdateSpecTokens   int            `json:"workUpdateSpecTokens,omitempty"`
	WorkUpdateCallTokens   int            `json:"workUpdateCallTokens,omitempty"`
	WorkUpdateResultTokens int            `json:"workUpdateResultTokens,omitempty"`
	P0PacketTokens         int            `json:"p0PacketTokens,omitempty"`
	P1PacketTokens         int            `json:"p1PacketTokens,omitempty"`
	ConversationTokens     int            `json:"conversationTokens"` // messages excluding tool results and packets
	// Packet tokens are carved out of ConversationTokens; both are zero when the
	// history path built the request.
	WorkPacketTokens         int `json:"workPacketTokens,omitempty"`
	EvidencePacketTokens     int `json:"evidencePacketTokens,omitempty"`
	ToolResultTokens         int `json:"toolResultTokens"` // tool result bodies
	ToolResultOriginalTokens int `json:"toolResultOriginalTokens,omitempty"`
	ToolResultSavedTokens    int `json:"toolResultSavedTokens,omitempty"`
	ToolResultsReduced       int `json:"toolResultsReduced,omitempty"`
	TotalTokens              int `json:"totalTokens"`
}

// measureRequest estimates the token cost of a request by layer, using the same
// estimator as context trimming. layers are the parts of req.System.
func measureRequest(layers []promptLayer, req llm.Request, packets RequestPackets, reductions ToolReductions) RequestBreakdown {
	b := RequestBreakdown{Layers: map[string]int{}, ToolCount: len(req.Tools)}
	occurrences := make(map[string]int)
	toolOriginal := 0
	for _, l := range layers {
		n := tokens(l.Text)
		b.Layers[l.Name] += n
		b.SystemTokens += n
	}
	b.ToolSpecTokens = toolSpecTokens(req.Tools)
	// Registration is feature gated. Without an exposed work.update spec all
	// attribution remains zero, even if older history still contains calls.
	enabled := false
	for _, spec := range req.Tools {
		if tools.FromWire(spec.Name) == "work.update" {
			enabled = true
			b.WorkUpdateSpecTokens += toolSpecTokens([]llm.ToolSpec{spec})
		}
	}
	// Consume call identities in message order. Consuming a pair permits reused
	// IDs without trusting a result's optional (or misleading) ToolName.
	pending := map[string][]bool{}
	for _, m := range req.Messages {
		if enabled && m.Role == llm.RoleAssistant {
			for _, call := range m.ToolCalls {
				match := tools.FromWire(call.Name) == "work.update"
				pending[call.ID] = append(pending[call.ID], match)
				if match {
					b.WorkUpdateCallTokens += tokens(call.Name) + tokens(string(call.Args))
				}
			}
		}
		if m.Role == llm.RoleTool {
			key := nextToolReductionKey(occurrences, m.ToolCallID)
			sent := tokens(m.Result)
			if queue := pending[m.ToolCallID]; len(queue) > 0 {
				if queue[0] {
					b.WorkUpdateResultTokens += sent
				}
				pending[m.ToolCallID] = queue[1:]
			}
			b.ToolResultTokens += sent
			toolOriginal += sent
			if report, ok := reductions[key]; ok && report.Strategy != "" && report.OriginalTokens > report.SentTokens {
				toolOriginal += report.OriginalTokens - sent
				b.ToolResultSavedTokens += report.OriginalTokens - report.SentTokens
				b.ToolResultsReduced++
			}
		}
	}
	if reductions != nil {
		b.ToolResultOriginalTokens = toolOriginal
	}
	b.ConversationTokens = estimateMessageTokens(req.Messages) - b.ToolResultTokens
	b.TotalTokens = b.SystemTokens + b.ToolSpecTokens + b.ConversationTokens + b.ToolResultTokens
	b.WorkPacketTokens, b.EvidencePacketTokens = packets.Work, packets.Evidence
	b.P0PacketTokens, b.P1PacketTokens = packets.P0, packets.P1
	b.ConversationTokens -= packets.Work + packets.Evidence
	return b
}
