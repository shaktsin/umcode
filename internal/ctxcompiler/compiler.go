// Package ctxcompiler builds the messages for one model call from what the
// engine has recorded about a unit of work, instead of replaying the
// transcript. It is pure: no store, no engine, no I/O, so every rule here is
// table-testable. A caller that gets false must use its existing history path.
package ctxcompiler

import (
	"strings"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/retrieval"
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
	// messageOverhead is the engine's flat per-message cost, counted here so
	// both sides of the history comparison measure the same way.
	messageOverhead = 4
)

// Input is everything Compile needs. The caller reads it from its store.
type Input struct {
	Retrieval      []retrieval.Candidate
	RetrievalQuery retrieval.Query
	// DesignedWorkflow enables the persisted semantic graph projection.
	DesignedWorkflow bool
	// Detail is the thread's open work. A zero value means there is nothing to
	// compile and Compile declines.
	Detail protocol.WorkDetail
	// Stale marks criterion node ids whose latest pass no longer holds. The
	// caller computes it, because a status stored on the node is only written
	// back at turn end and is stale-blind in the middle of a turn.
	Stale map[string]bool
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
	RetrievalTokens      int      `json:"retrievalTokens,omitempty"`
	RetrievalIDs         []string `json:"retrievalIDs,omitempty"`
	Criteria             int      `json:"criteria"`
	Evidence             int      `json:"evidence"`
	TailMessages         int      `json:"tailMessages"`
	WorkPacketTokens     int      `json:"workPacketTokens"`
	EvidencePacketTokens int      `json:"evidencePacketTokens"`
	TailTokens           int      `json:"tailTokens"`
	P0Tokens             int      `json:"p0Tokens,omitempty"`
	P1Tokens             int      `json:"p1Tokens,omitempty"`
	Drops                []Drop   `json:"drops,omitempty"`
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

// estimate counts the tokens of a message list exactly as the engine's own
// estimator does, including its flat per-message overhead. The two must agree,
// because Compile compares its output against an engine-measured history size.
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
		for _, t := range m.Thinking {
			n += int(llm.EstimateTokens(t.Text))
		}
		for _, c := range m.ToolCalls {
			n += int(llm.EstimateTokens(c.Name)) + int(llm.EstimateTokens(string(c.Args)))
		}
		n += int(llm.EstimateTokens(m.Result))
		n += messageOverhead
	}
	return n
}

// packetHeading introduces the compiled work state. It is a user message
// because providers accept plain user text from any role sequence.
const packetHeading = "Current work state"

// Compile builds the request prefix: one message holding the work and evidence
// packets, then the interaction tail. The caller appends the new user message.
// The second result is false when the input cannot be trusted to produce a
// better request than the caller's history path; Report.Declined then says why.
func Compile(in Input) (Result, bool) {
	if in.Detail.Work.Goal == "" {
		return Result{Report: Report{Declined: "no goal"}}, false
	}
	var work, evidence string
	var criteria, rows int
	var p2 []int
	p0Tokens, p1Tokens := 0, 0
	var drops []Drop
	if in.DesignedWorkflow {
		p, err := designedPacket(in.Detail, in.Stale, in.Active)
		if err != nil {
			return Result{Report: Report{Declined: "invalid designed projection"}}, false
		}
		p0Tokens = textTokens(p.p0)
		budget := packetBudget(in.Window)
		if in.Window > 0 && p0Tokens > budget {
			return Result{Report: Report{Declined: "P0 over budget", Criteria: p.criteria, P0Tokens: p0Tokens}}, false
		}
		if budget > 0 && textTokens(p.p0+p.p1)+textTokens(p.evidence) > budget {
			drops = append(drops, Drop{Class: "designed P1", Reason: "packet budget", Count: p.supporting + p.rows})
			p.p1, p.evidence, p.rows = "", "", 0
		}
		work, criteria, evidence, rows, p2 = p.p0+p.p1, p.criteria, p.evidence, p.rows, nil
		p1Tokens = textTokens(work) - p0Tokens + textTokens(evidence)
	} else {
		work, criteria = workPacket(in.Detail, in.Stale)
		evidence, rows, p2 = evidencePacket(in.Detail, in.Active, in.Stale)
	}

	budget := packetBudget(in.Window)
	if budget > 0 {
		if textTokens(work) > budget {
			return Result{Report: Report{Declined: "P0 over budget", Criteria: criteria}}, false
		}
		var dropped []Drop
		evidence, rows, dropped = fitEvidence(evidence, rows, p2, budget-textTokens(work))
		drops = append(drops, dropped...)
		// Failure lines are never dropped, so the packets can still be over the
		// ceiling. The budget is a promise, so decline rather than break it.
		if textTokens(work)+textTokens(evidence) > budget {
			return Result{Report: Report{Declined: "packets over budget", Criteria: criteria, Evidence: rows, Drops: drops}}, false
		}
	}

	head := packetHeading + "\n\n" + work
	if evidence != "" {
		head += "\n" + evidence
	}
	msgs := []llm.Message{llm.Text(llm.RoleUser, head)}
	tailMsgs, tailDropped, excluded := tailWithIDs(in.Items, in.TurnID, tailBudget(in.Window))
	retrievalTokens := 0
	var retrievalIDs []string
	if len(in.Retrieval) > 0 {
		for _, n := range in.Detail.Nodes {
			if strings.Contains(head, "- "+n.ID+" ") {
				excluded["node:"+n.ID] = true
			}
		}
		for _, ev := range in.Detail.Evidence {
			if strings.Contains(head, "- "+ev.ID+" ") || ev.Summary != "" && strings.Contains(head, ev.Summary) {
				excluded["evidence:"+ev.ID] = true
			}
		}
		remaining := retrieval.MaxTokens
		if in.Window > 0 {
			remaining = min(remaining, share(in.Window, 0.05), packetBudget(in.Window)-textTokens(head)-1)
		}
		entries, _, err := retrieval.Select(in.RetrievalQuery, in.Retrieval, excluded, max(0, remaining))
		if err == nil && len(entries) > 0 {
			addition := "\n" + retrieval.Render(entries)
			candidate := head + addition
			if in.Window <= 0 || textTokens(candidate) <= packetBudget(in.Window) {
				retrievalTokens = textTokens(candidate) - textTokens(head)
				msgs[0] = llm.Text(llm.RoleUser, candidate)
				for _, entry := range entries {
					retrievalIDs = append(retrievalIDs, entry.ID)
				}
			}
		}
	}
	msgs = append(msgs, tailMsgs...)
	if tb := tailBudget(in.Window); tb > 0 && estimate(tailMsgs) > tb {
		drops = append(drops, Drop{Class: "tail", Reason: "one exchange exceeds the tail budget", Count: 1})
	}
	if tailDropped > 0 {
		drops = append(drops, Drop{Class: "tool and file-change notes", Reason: "carried by the work packet", Count: tailDropped})
	}

	rep := Report{
		Criteria: criteria, Evidence: rows, TailMessages: len(tailMsgs),
		RetrievalTokens: retrievalTokens, RetrievalIDs: retrievalIDs,
		WorkPacketTokens: textTokens(work), EvidencePacketTokens: textTokens(evidence),
		TailTokens: estimate(tailMsgs), Drops: drops,
		P0Tokens: p0Tokens, P1Tokens: p1Tokens,
	}
	if in.HistoryTokens > 0 && estimate(msgs) >= in.HistoryTokens {
		rep.Declined = "not smaller than history"
		return Result{Report: rep}, false
	}
	return Result{Messages: msgs, Report: rep}, true
}

// textTokens estimates one packet's cost.
func textTokens(s string) int { return int(llm.EstimateTokens(s)) }

// fitEvidence trims the evidence packet to budget: passing lines (P2) go first,
// then the oldest tool errors (P1). Failure lines are never dropped here; when
// nothing is left to drop the packet goes over and the caller decides.
func fitEvidence(text string, rows int, p2 []int, budget int) (string, int, []Drop) {
	if text == "" || budget <= 0 || textTokens(text) <= budget {
		return text, rows, nil
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	drop := map[int]bool{}
	var drops []Drop

	count := 0
	for _, i := range p2 {
		if i < len(lines) {
			drop[i] = true
			count++
		}
		if textTokens(render(lines, drop)) <= budget {
			break
		}
	}
	if count > 0 {
		drops = append(drops, Drop{Class: "passing evidence", Reason: "packet budget", Count: count})
	}
	count = 0
	for i := len(lines) - 1; i >= 0 && textTokens(render(lines, drop)) > budget; i-- {
		if drop[i] || !strings.HasPrefix(lines[i], "- tool ") {
			continue
		}
		drop[i] = true
		count++
	}
	if count > 0 {
		drops = append(drops, Drop{Class: "tool errors", Reason: "packet budget", Count: count})
	}
	return render(lines, drop), rows - len(drop), drops
}

// render rebuilds a packet without the dropped lines, and returns "" when only
// the heading would remain.
func render(lines []string, drop map[int]bool) string {
	var kept []string
	body := 0
	for i, l := range lines {
		if drop[i] {
			continue
		}
		kept = append(kept, l)
		if strings.HasPrefix(l, "- ") {
			body++
		}
	}
	if body == 0 {
		return ""
	}
	return strings.Join(kept, "\n") + "\n"
}
