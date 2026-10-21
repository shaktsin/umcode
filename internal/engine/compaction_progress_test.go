package engine

import (
	"context"
	"errors"
	"github.com/shaktsin/umcode/internal/llm"
	"github.com/shaktsin/umcode/internal/models"
	"github.com/shaktsin/umcode/internal/protocol"
	"strings"
	"testing"
)

func TestCompactionTerminatesOnFailureAndCancel(t *testing.T) {
	for _, cause := range []error{errors.New("failure"), context.Canceled} {
		e, turn := liveFixture(t)
		p, err := e.beginCompaction(t.Context(), turn)
		if err != nil {
			t.Fatal(err)
		}
		if err = p.finish(t.Context(), cause); err != nil {
			t.Fatal(err)
		}
		got, err := e.Store.LatestCompactionState(t.Context(), turn.ThreadID)
		want := "failed"
		if errors.Is(cause, context.Canceled) {
			want = "cancelled"
		}
		if err != nil || got.Status != want {
			t.Fatal(got, err)
		}
	}
}
func TestCompactionSnapshotAfterReconnect(t *testing.T) {
	e, turn := liveFixture(t)
	p, err := e.beginCompaction(t.Context(), turn)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.phase(t.Context(), "summarizing"); err != nil {
		t.Fatal(err)
	}
	read, err := e.ReadThread(t.Context(), turn.ThreadID)
	if err != nil || read.Compaction == nil || read.Compaction.Status != "summarizing" || read.Compaction.Sequence != 2 {
		t.Fatal(read.Compaction, err)
	}
}
func TestCompactionCompletesAfterCheckpointInstallation(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		e, turn := liveFixture(t)
		if failWrite {
			if _, err := e.Store.DB.ExecContext(t.Context(), `CREATE TRIGGER reject_checkpoint BEFORE INSERT ON items WHEN NEW.kind='contextCompaction' BEGIN SELECT RAISE(ABORT,'checkpoint failed'); END`); err != nil {
				t.Fatal(err)
			}
		}
		res := resolved{sel: protocol.ModelSelection{Provider: "fixture", Model: "small"}, meta: models.Meta{ContextWindow: 8192}}
		req := llm.Request{Messages: []llm.Message{{Role: llm.RoleTool, ToolCallID: "c", Result: strings.Repeat("x", 100000)}}, MaxTokens: 2048}
		_, _, err := e.prepareModelRequest(t.Context(), t.Context(), turn, &res, req, "chat", newTurnBudget(protocol.ExecutionLimits{}))
		if (err != nil) != failWrite {
			t.Fatal(err)
		}
		state, readErr := e.Store.LatestCompactionState(t.Context(), turn.ThreadID)
		if readErr != nil || state == nil {
			t.Fatal(state, readErr)
		}
		if failWrite {
			if state.Status != "failed" {
				t.Fatal(state)
			}
		} else {
			if state.Status != "completed" {
				t.Fatal(state)
			}
			items, err := e.Store.ListItems(t.Context(), turn.ThreadID, 0)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, it := range items {
				if it.Kind == protocol.ItemContextCompaction {
					found = true
				}
			}
			if !found {
				t.Fatal("reported complete without installed checkpoint")
			}
		}
	}
}
