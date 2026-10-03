// Package ctxcompiler builds the messages for one model call from what the
// engine has recorded about a unit of work, instead of replaying the
// transcript. It is pure: no store, no engine, no I/O, so every rule here is
// table-testable. A caller that gets false must use its existing history path.
package ctxcompiler

import (
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

const (
	// packetFraction is the share of the model's context window the work and
	// evidence packets may occupy together.
	packetFraction = 0.15
	// tailFraction is the share the interaction tail may occupy.
	tailFraction = 0.25
	// tailPairs is how many recent user messages, with the assistant messages
	// between them, the tail keeps.
	tailPairs = 10
	// imageTokens is the flat cost of an image part, matching the engine's own
	// estimator: a base64 length says little about the real cost.
	imageTokens = 1500
)

// Input is everything Compile needs. The caller reads it from its store.
type Input struct {
	// Detail is the thread's open work. A zero value means there is nothing to
	// compile and Compile declines.
	Detail protocol.WorkDetail
	// Active is the evidence that is still true. Nil means "every row that is
	// not stale", which keeps this package usable without the work service.
	Active []protocol.Evidence
	// Items is the thread's transcript, oldest first.
	Items []protocol.Item
	// TurnID is the running turn, whose items the tail skips.
	TurnID string
	// Window is the model's context window in tokens; zero means unbounded.
	Window int
	// HistoryTokens is the estimated size of the caller's history path. When it
	// is non-zero, Compile declines rather than returning something larger.
	HistoryTokens int
}

// Drop records content left out of a packet, for the caller's log.
type Drop struct {
	Class  string `json:"class"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// Report explains one compilation. It is diagnostic only.
type Report struct {
	Criteria             int    `json:"criteria"`
	Evidence             int    `json:"evidence"`
	TailMessages         int    `json:"tailMessages"`
	WorkPacketTokens     int    `json:"workPacketTokens"`
	EvidencePacketTokens int    `json:"evidencePacketTokens"`
	TailTokens           int    `json:"tailTokens"`
	Drops                []Drop `json:"drops,omitempty"`
	// Declined names why compilation was refused, and is empty on success.
	Declined string `json:"declined,omitempty"`
}

// Result is the compiled request prefix: the work state, then the tail. The
// caller appends the new user message itself.
type Result struct {
	Messages []llm.Message
	Report   Report
}

// packetBudget is the token ceiling for both packets together. Zero means no
// ceiling.
func packetBudget(window int) int { return share(window, packetFraction) }

// tailBudget is the token ceiling for the interaction tail. Zero means no
// ceiling.
func tailBudget(window int) int { return share(window, tailFraction) }

func share(window int, fraction float64) int {
	if window <= 0 {
		return 0
	}
	return int(float64(window) * fraction)
}

// estimate counts the tokens of a message list the same way the engine does.
func estimate(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.Type == "image" {
				n += imageTokens
				continue
			}
			n += int(llm.EstimateTokens(p.Text))
		}
		n += int(llm.EstimateTokens(m.Result))
	}
	return n
}

// Compile builds the request prefix. The second result is false when the input
// cannot be trusted to produce a better request than the caller's history path;
// Report.Declined then says why.
func Compile(in Input) (Result, bool) {
	if in.Detail.Work.Goal == "" {
		return Result{Report: Report{Declined: "no goal"}}, false
	}
	return Result{Report: Report{Declined: "not implemented"}}, false
}
