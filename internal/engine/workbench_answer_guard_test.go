package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func TestWorkbenchAnswerAllowsActualStartResult(t *testing.T) {
	id := "train-" + strings.Repeat("b", 32)
	c := &protocol.WorkbenchTurnContext{StartedRunIDs: []string{id}}
	answer := "I started " + id + "; it is running."
	if got := (&Engine{}).guardWorkbenchAnswer(withWorkbenchContext(context.Background(), c), answer); got != answer {
		t.Fatalf("guarded actual start: %q", got)
	}
}
