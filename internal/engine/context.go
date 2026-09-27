package engine

import (
	"fmt"

	"github.com/shaktsin/umcode/internal/llm"
)

const (
	// contextTrimFraction is the share of the model's context window at which
	// old tool output is dropped from the request.
	contextTrimFraction = 0.75
	// contextCompactFraction is the share at which a new turn first summarizes
	// the earlier conversation.
	contextCompactFraction = 0.70
	// keepRecentToolResults is how many of the newest tool results survive
	// trimming untouched.
	keepRecentToolResults = 6
	// minTrimmedResult is the size below which a tool result is not worth
	// replacing with a stub.
	minTrimmedResult = 512
)

// estimateMessageTokens is a rough token count for a request's messages. Images
// count a flat amount because their base64 size says little about their cost.
func estimateMessageTokens(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.Type == "image" {
				n += 1500
				continue
			}
			n += tokens(p.Text)
		}
		for _, t := range m.Thinking {
			n += tokens(t.Text)
		}
		for _, c := range m.ToolCalls {
			n += tokens(c.Name) + tokens(string(c.Args))
		}
		n += tokens(m.Result)
		n += 4
	}
	return n
}

// trimToolResults replaces the bodies of older, large tool results with a short
// stub until the estimate fits under limit tokens (system counts as overhead).
// The newest keepRecentToolResults results are never touched, and messages keep
// their order so every tool call still has its result. It reports how many
// results it dropped.
func trimToolResults(msgs []llm.Message, overhead, limit int) int {
	if limit <= 0 || estimateMessageTokens(msgs)+overhead <= limit {
		return 0
	}
	var idx []int
	for i, m := range msgs {
		if m.Role == llm.RoleTool {
			idx = append(idx, i)
		}
	}
	dropped := 0
	for k := 0; k < len(idx)-keepRecentToolResults; k++ {
		m := &msgs[idx[k]]
		if len(m.Result) < minTrimmedResult {
			continue
		}
		m.Result = fmt.Sprintf("[tool result omitted to save context: %d bytes from %s. Re-run the tool if you need it again.]", len(m.Result), m.ToolName)
		dropped++
		if estimateMessageTokens(msgs)+overhead <= limit {
			break
		}
	}
	return dropped
}

// overWindow reports whether the messages plus overhead exceed the fraction of
// the model's context window. It is false when the window is unknown.
func overWindow(msgs []llm.Message, overhead, window int, fraction float64) bool {
	if window <= 0 {
		return false
	}
	return float64(estimateMessageTokens(msgs)+overhead) > float64(window)*fraction
}

func tokens(s string) int { return int(llm.EstimateTokens(s)) }
