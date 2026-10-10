package ctxcompiler

import (
	"strings"

	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/protocol"
)

// compactionPrefix introduces a summary written by an explicit compaction. The
// wording matches the engine's history path, so a compacted thread reads the
// same either way.
const compactionPrefix = "Earlier conversation summary (the full transcript remains visible in UMCode):\n"

// turnMessage is one kept transcript entry before role merging.
type turnMessage struct {
	id   string
	role llm.Role
	text string
}

// tail returns the recent conversation the model needs to read the current
// request: the newest tailPairs exchanges, plus the question-and-answer pair the
// request may be answering. Tool calls and file changes are left out, because
// the work packet already carries them. The second result counts those
// omissions. A budget of zero means unbounded.
func tail(items []protocol.Item, currentTurn string, budget int) ([]llm.Message, int) {
	msgs, dropped, _ := tailWithIDs(items, currentTurn, budget)
	return msgs, dropped
}

func tailWithIDs(items []protocol.Item, currentTurn string, budget int) ([]llm.Message, int, map[string]bool) {
	kept, dropped, summary := collect(items, currentTurn)
	idx := userIndexes(kept)
	if len(idx) > tailPairs {
		start := idx[len(idx)-tailPairs]
		kept = withAnsweredQuestion(kept, start)
	}
	msgs := assemble(kept, summary)
	for budget > 0 && estimate(msgs) > budget {
		trimmed, ok := dropOldestPair(kept)
		if !ok {
			break
		}
		kept = trimmed
		msgs = assemble(kept, summary)
	}
	ids := map[string]bool{}
	started := summary != ""
	for _, m := range kept {
		if m.role == llm.RoleUser {
			started = true
		}
		if started && m.id != "" {
			ids["item:"+m.id] = true
		}
	}
	return msgs, dropped, ids
}

// collect keeps the conversation entries, counts the omitted tool and
// file-change items, and restarts at the newest compaction summary.
func collect(items []protocol.Item, currentTurn string) (kept []turnMessage, dropped int, summary string) {
	for _, it := range items {
		if it.TurnID == currentTurn || it.Status == protocol.ItemInProgress {
			continue
		}
		switch it.Kind {
		case protocol.ItemUserMessage, protocol.ItemInboundEvent:
			if it.Text != "" {
				kept = append(kept, turnMessage{id: it.ID, role: llm.RoleUser, text: it.Text})
			}
		case protocol.ItemAgentMessage:
			if it.Text != "" && it.Status == protocol.ItemCompleted {
				kept = append(kept, turnMessage{id: it.ID, role: llm.RoleAssistant, text: it.Text})
			}
		case protocol.ItemToolCall, protocol.ItemFileChange:
			dropped++
		case protocol.ItemContextCompaction:
			// Everything before the newest summary is already in it.
			kept, summary = nil, it.Text
		}
	}
	return kept, dropped, summary
}

func userIndexes(kept []turnMessage) []int {
	var idx []int
	for i, m := range kept {
		if m.role == llm.RoleUser {
			idx = append(idx, i)
		}
	}
	return idx
}

// withAnsweredQuestion keeps the window from start, and prepends the older
// question and its answer when the newest assistant message asked something:
// a bare "yes" or "the second one" is unreadable without it. A question that
// opens the transcript, with no user message before it, is left out, because a
// request cannot begin with an assistant message.
func withAnsweredQuestion(kept []turnMessage, start int) []turnMessage {
	window := kept[start:]
	// Only a question whose answer sits at the edge of the window can be what
	// the current request is replying to. An older one is noise.
	q := lastQuestion(kept[:start])
	if q < 0 {
		return window
	}
	if a := nextUser(kept, q); a < 0 || a < start-1 {
		return window
	}
	var extra []turnMessage
	// The question is an assistant message, and a request must start with a user
	// message, so the message that prompted the question comes along with it.
	if q > 0 && kept[q-1].role == llm.RoleUser {
		extra = append(extra, kept[q-1])
	}
	extra = append(extra, kept[q])
	if a := nextUser(kept, q); a >= 0 && a < start {
		extra = append(extra, kept[a])
	}
	return append(extra, window...)
}

// lastQuestion returns the index in kept of the newest assistant message whose
// last non-empty line ends with a question mark, or -1.
func lastQuestion(kept []turnMessage) int {
	for i := len(kept) - 1; i >= 0; i-- {
		if kept[i].role == llm.RoleAssistant && endsWithQuestion(kept[i].text) {
			return i
		}
	}
	return -1
}

// nextUser returns the index of the first user message after i, or -1.
func nextUser(kept []turnMessage, i int) int {
	for j := i + 1; j < len(kept); j++ {
		if kept[j].role == llm.RoleUser {
			return j
		}
	}
	return -1
}

// endsWithQuestion reports whether the last non-empty line of text ends with a
// question mark.
func endsWithQuestion(text string) bool {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return strings.HasSuffix(l, "?")
		}
	}
	return false
}

// assemble merges consecutive same-role text, puts the compaction summary
// first, drops leading assistant messages and appends the filler reply a
// trailing user message needs, matching what providers accept.
func assemble(kept []turnMessage, summary string) []llm.Message {
	var msgs []llm.Message
	if summary != "" {
		msgs = append(msgs, llm.Text(llm.RoleUser, compactionPrefix+summary))
	}
	for _, m := range kept {
		msgs = appendText(msgs, m.role, m.text)
	}
	for len(msgs) > 0 && msgs[0].Role != llm.RoleUser {
		msgs = msgs[1:]
	}
	if n := len(msgs); n > 0 && msgs[n-1].Role == llm.RoleUser {
		msgs = appendText(msgs, llm.RoleAssistant, "(no reply)")
	}
	return msgs
}

// appendText merges consecutive same-role text messages, as the engine's
// history path does.
func appendText(msgs []llm.Message, role llm.Role, text string) []llm.Message {
	if n := len(msgs); n > 0 && msgs[n-1].Role == role {
		msgs[n-1].Parts[0].Text += "\n\n" + text
		return msgs
	}
	return append(msgs, llm.Text(role, text))
}

// dropOldestPair removes everything up to and including the second-oldest user
// message's predecessor, so whole exchanges leave together and the newest one
// always survives.
func dropOldestPair(kept []turnMessage) ([]turnMessage, bool) {
	idx := userIndexes(kept)
	if len(idx) < 2 {
		return kept, false
	}
	return kept[idx[1]:], true
}
