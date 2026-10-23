package store

import (
	"github.com/shaktsin/umcode/internal/protocol"
	"testing"
	"time"
)

func TestCompactionLifecycleOrdersUpdates(t *testing.T) {
	st, th := workFixture(t)
	now := time.Now().UTC()
	state := protocol.CompactionState{ID: "a", ThreadID: th.ID, TurnID: "t", Sequence: 1, Status: "preparing", StartedAt: now, UpdatedAt: now}
	if err := st.SaveCompactionState(t.Context(), state); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCompactionState(t.Context(), state); err == nil {
		t.Fatal("stale sequence accepted")
	}
	state.Sequence = 2
	state.Status = "completed"
	if err := st.SaveCompactionState(t.Context(), state); err != nil {
		t.Fatal(err)
	}
	state.Sequence = 3
	state.Status = "summarizing"
	if err := st.SaveCompactionState(t.Context(), state); err == nil {
		t.Fatal("terminal state reopened")
	}
	next := state
	next.ID = "b"
	next.Sequence = 1
	next.StartedAt = now.Add(time.Second)
	next.UpdatedAt = next.StartedAt
	next.Status = "preparing"
	if err := st.SaveCompactionState(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	state.Sequence = 99
	if err := st.SaveCompactionState(t.Context(), state); err == nil {
		t.Fatal("older attempt replaced current")
	}
	got, err := st.LatestCompactionState(t.Context(), th.ID)
	if err != nil || got.ID != "b" {
		t.Fatal(got, err)
	}
}
func TestCompactionStartupSettlesInterruptedWork(t *testing.T) {
	st, th := workFixture(t)
	now := time.Now().UTC()
	s := protocol.CompactionState{ID: "a", ThreadID: th.ID, TurnID: "t", Sequence: 1, Status: "summarizing", StartedAt: now, UpdatedAt: now}
	if err := st.SaveCompactionState(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if err := st.ReconcileCompactions(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := st.LatestCompactionState(t.Context(), th.ID)
	if err != nil || got.Status != "interrupted" || got.Sequence != 2 {
		t.Fatal(got, err)
	}
}
