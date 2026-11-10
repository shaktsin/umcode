package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/shaktsin/umcode/internal/protocol"
)

func suggestionFixture(t *testing.T) (*Store, string) {
	t.Helper()
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	th, err := s.CreateThread(t.Context(), protocol.Thread{Title: "suggest"})
	if err != nil {
		t.Fatal(err)
	}
	return s, th.ID
}

func TestSuggestionOnePerTurn(t *testing.T) {
	s, th := suggestionFixture(t)
	a, err := s.CreateAgentSuggestion(t.Context(), AgentSuggestion{ThreadID: th, TurnID: "t1", Reason: "big task"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == "" || a.State != SuggestionPending || a.CreatedAt.IsZero() || a.DecidedAt != nil {
		t.Fatalf("got %+v", a)
	}
	if _, err = s.CreateAgentSuggestion(t.Context(), AgentSuggestion{ThreadID: th, TurnID: "t1", Reason: "again"}); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("want ErrAgentConflict, got %v", err)
	}
	if _, err = s.CreateAgentSuggestion(t.Context(), AgentSuggestion{ThreadID: th, TurnID: "t2"}); err != nil {
		t.Fatalf("other turn: %v", err)
	}
	got, err := s.LatestAgentSuggestion(t.Context(), th)
	if err != nil || got.TurnID != "t2" {
		t.Fatalf("latest: %+v %v", got, err)
	}
	if _, err = s.LatestAgentSuggestion(t.Context(), "nothread"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSuggestionDecisionTransitions(t *testing.T) {
	s, th := suggestionFixture(t)
	mk := func(turn string) AgentSuggestion {
		a, err := s.CreateAgentSuggestion(t.Context(), AgentSuggestion{ThreadID: th, TurnID: turn})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a := mk("t1")
	d, err := s.DecideAgentSuggestion(t.Context(), a.ID, SuggestionEnabled)
	if err != nil || d.State != SuggestionEnabled || d.DecidedAt == nil {
		t.Fatalf("decide: %+v %v", d, err)
	}
	again, err := s.DecideAgentSuggestion(t.Context(), a.ID, SuggestionEnabled)
	if err != nil || again.State != SuggestionEnabled || !again.DecidedAt.Equal(*d.DecidedAt) {
		t.Fatalf("idempotent: %+v %v", again, err)
	}
	for _, st := range []string{SuggestionDeclined, SuggestionPending, SuggestionExpired, "bogus"} {
		if _, err = s.DecideAgentSuggestion(t.Context(), a.ID, st); !errors.Is(err, ErrAgentTransition) {
			t.Fatalf("enabled->%s: want ErrAgentTransition, got %v", st, err)
		}
	}
	b := mk("t2")
	if _, err = s.DecideAgentSuggestion(t.Context(), b.ID, SuggestionPending); !errors.Is(err, ErrAgentTransition) {
		t.Fatalf("pending->pending: %v", err)
	}
	if _, err = s.DecideAgentSuggestion(t.Context(), b.ID, SuggestionExpired); !errors.Is(err, ErrAgentTransition) {
		t.Fatalf("pending->expired via decide: %v", err)
	}
	if _, err = s.DecideAgentSuggestion(t.Context(), b.ID, SuggestionDeclined); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DecideAgentSuggestion(t.Context(), b.ID, SuggestionEnabled); !errors.Is(err, ErrAgentTransition) {
		t.Fatalf("declined->enabled: %v", err)
	}
	if _, err = s.DecideAgentSuggestion(t.Context(), "missing", SuggestionEnabled); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestDeclineSuppressesUntilCleared(t *testing.T) {
	s, th := suggestionFixture(t)
	if ok, err := s.ThreadDeclinedSuggestion(t.Context(), th); err != nil || ok {
		t.Fatalf("fresh: %v %v", ok, err)
	}
	a, _ := s.CreateAgentSuggestion(t.Context(), AgentSuggestion{ThreadID: th, TurnID: "t1"})
	if ok, _ := s.ThreadDeclinedSuggestion(t.Context(), th); ok {
		t.Fatal("pending must not suppress")
	}
	if _, err := s.DecideAgentSuggestion(t.Context(), a.ID, SuggestionDeclined); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ThreadDeclinedSuggestion(t.Context(), th); err != nil || !ok {
		t.Fatalf("declined: %v %v", ok, err)
	}
	if err := s.ClearThreadDeclines(t.Context(), th); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ThreadDeclinedSuggestion(t.Context(), th); err != nil || ok {
		t.Fatalf("cleared: %v %v", ok, err)
	}
	// The decision stays on record, and the one-per-turn rule still holds.
	got, err := s.LatestAgentSuggestion(t.Context(), th)
	if err != nil || got.State != SuggestionDeclined {
		t.Fatalf("history: %+v %v", got, err)
	}
	if _, err = s.CreateAgentSuggestion(t.Context(), AgentSuggestion{ThreadID: th, TurnID: "t1"}); !errors.Is(err, ErrAgentConflict) {
		t.Fatalf("same turn: %v", err)
	}
	// An explicit enabled response after a decline does not re-suppress.
	b, _ := s.CreateAgentSuggestion(t.Context(), AgentSuggestion{ThreadID: th, TurnID: "t2"})
	if _, err = s.DecideAgentSuggestion(t.Context(), b.ID, SuggestionEnabled); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ThreadDeclinedSuggestion(t.Context(), th); ok {
		t.Fatal("cleared decline must stay cleared")
	}
	// A later decline suppresses again.
	c, _ := s.CreateAgentSuggestion(t.Context(), AgentSuggestion{ThreadID: th, TurnID: "t3"})
	if _, err = s.DecideAgentSuggestion(t.Context(), c.ID, SuggestionDeclined); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ThreadDeclinedSuggestion(t.Context(), th); !ok {
		t.Fatal("new decline must suppress")
	}
}

func TestExpirePendingOnRestart(t *testing.T) {
	s, th := suggestionFixture(t)
	p, _ := s.CreateAgentSuggestion(t.Context(), AgentSuggestion{ThreadID: th, TurnID: "t1"})
	e, _ := s.CreateAgentSuggestion(t.Context(), AgentSuggestion{ThreadID: th, TurnID: "t2"})
	if _, err := s.DecideAgentSuggestion(t.Context(), e.ID, SuggestionEnabled); err != nil {
		t.Fatal(err)
	}
	n, err := s.ExpirePendingAgentSuggestions(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("expired %d %v", n, err)
	}
	var st string
	if err = s.DB.QueryRow(`SELECT state FROM agent_suggestions WHERE id=?`, p.ID).Scan(&st); err != nil || st != SuggestionExpired {
		t.Fatalf("state %q %v", st, err)
	}
	if err = s.DB.QueryRow(`SELECT state FROM agent_suggestions WHERE id=?`, e.ID).Scan(&st); err != nil || st != SuggestionEnabled {
		t.Fatalf("enabled changed: %q", st)
	}
	if n, err = s.ExpirePendingAgentSuggestions(t.Context()); err != nil || n != 0 {
		t.Fatalf("second: %d %v", n, err)
	}
	if _, err = s.DecideAgentSuggestion(t.Context(), p.ID, SuggestionEnabled); !errors.Is(err, ErrAgentTransition) {
		t.Fatalf("expired decide: %v", err)
	}
}
