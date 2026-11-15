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

func TestWorkbenchAnswerBlocksUnbackedDatasetClaim(t *testing.T) {
	c := &protocol.WorkbenchTurnContext{}
	for _, answer := range []string{
		"I created the dataset reviews-balanced with 6,696 rows.",
		"I've generated a balanced dataset for you.",
		"We made a new dataset from the three files.",
		"I’ve created the dataset reviews-balanced.",
	} {
		if got := (&Engine{}).guardWorkbenchAnswer(withWorkbenchContext(context.Background(), c), answer); got != unverifiedDatasetAnswer {
			t.Fatalf("unbacked claim %q was shown as %q", answer, got)
		}
	}
}

func TestWorkbenchAnswerAllowsBackedDatasetClaim(t *testing.T) {
	c := &protocol.WorkbenchTurnContext{CreatedDatasetIDs: []string{strings.Repeat("a", 64)}}
	answer := "I created the dataset reviews-balanced with 6,696 rows."
	if got := (&Engine{}).guardWorkbenchAnswer(withWorkbenchContext(context.Background(), c), answer); got != answer {
		t.Fatalf("guarded a backed claim: %q", got)
	}
}

func TestWorkbenchAnswerLeavesPlainDatasetTalkAlone(t *testing.T) {
	c := &protocol.WorkbenchTurnContext{}
	for _, answer := range []string{
		"Your dataset has 6,696 rows and two classes.",
		"I can create a balanced dataset if you approve a script.",
	} {
		if got := (&Engine{}).guardWorkbenchAnswer(withWorkbenchContext(context.Background(), c), answer); got != answer {
			t.Fatalf("rewrote ordinary text %q as %q", answer, got)
		}
	}
}

func TestWorkbenchAnswerAllowsOrdinaryUseOfMadeAndBuilt(t *testing.T) {
	c := &protocol.WorkbenchTurnContext{}
	for _, answer := range []string{
		"I made sure the split uses the selected dataset.",
		"We built the model around your dataset.",
		"I created a plan to inspect the dataset first.",
	} {
		if got := (&Engine{}).guardWorkbenchAnswer(withWorkbenchContext(context.Background(), c), answer); got != answer {
			t.Fatalf("false positive on %q: %q", answer, got)
		}
	}
}
