package engine

import (
	"fmt"
	"unicode/utf8"
)

// Bound even the newest tool result before the next request. The original
// output stays in the transcript and is available through chat-scoped paging.
func boundedModelToolOutput(output, itemID string) string {
	const limit = 4096
	if len(output) <= limit || itemID == "" {
		return output
	}
	reference := fmt.Sprintf("\n[Excerpt only; full output is stored. Read it with task.context {\"item_id\":%q,\"offset\":0,\"limit\":512}.]\n", itemID)
	end := limit - len(reference)
	for end > 0 && !utf8.RuneStart(output[end]) {
		end--
	}
	return output[:end] + reference
}
