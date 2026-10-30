package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Suggestion states. A suggestion is the model's request, recorded as a card,
// that the user enable agents; it never enables anything itself.
const (
	SuggestionPending  = "pending"
	SuggestionEnabled  = "enabled"
	SuggestionDeclined = "declined"
	SuggestionExpired  = "expired"
)

const maxSuggestionReason = 2048

// AgentSuggestion is one agent.suggest record (at most one per thread turn).
type AgentSuggestion struct {
	ID, ThreadID, TurnID, Reason, State string
	CreatedAt                           time.Time
	DecidedAt                           *time.Time
}

const suggestionCols = `id,thread_id,turn_id,reason,state,created_at,decided_at`

func scanSuggestion(row interface{ Scan(...any) error }) (AgentSuggestion, error) {
	var a AgentSuggestion
	var created, decided string
	if err := row.Scan(&a.ID, &a.ThreadID, &a.TurnID, &a.Reason, &a.State, &created, &decided); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AgentSuggestion{}, ErrNotFound
		}
		return AgentSuggestion{}, err
	}
	a.CreatedAt = ParseTime(created)
	if decided != "" {
		t := ParseTime(decided)
		a.DecidedAt = &t
	}
	return a, nil
}

// CreateAgentSuggestion stores a pending suggestion. It returns ErrAgentConflict
// when the thread turn already has one.
func (s *Store) CreateAgentSuggestion(ctx context.Context, a AgentSuggestion) (AgentSuggestion, error) {
	if a.ThreadID == "" || a.TurnID == "" {
		return AgentSuggestion{}, fmt.Errorf("%w: thread and turn required", ErrAgentTransition)
	}
	reason := strings.TrimSpace(a.Reason)
	if len(reason) > maxSuggestionReason {
		reason = reason[:maxSuggestionReason]
	}
	if a.ID == "" {
		a.ID = NewID("sug")
	}
	now := time.Now().UTC()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO agent_suggestions(id,thread_id,turn_id,reason,state,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(thread_id,turn_id) DO NOTHING`,
		a.ID, a.ThreadID, a.TurnID, reason, SuggestionPending, FormatTime(now))
	if err != nil {
		return AgentSuggestion{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return AgentSuggestion{}, err
	} else if n != 1 {
		return AgentSuggestion{}, ErrAgentConflict
	}
	return scanSuggestion(s.DB.QueryRowContext(ctx, `SELECT `+suggestionCols+` FROM agent_suggestions WHERE id=?`, a.ID))
}

// DecideAgentSuggestion moves pending -> enabled|declined. Repeating the same
// decision returns the stored record unchanged; any other move is
// ErrAgentTransition.
func (s *Store) DecideAgentSuggestion(ctx context.Context, id, state string) (AgentSuggestion, error) {
	if state != SuggestionEnabled && state != SuggestionDeclined {
		return AgentSuggestion{}, fmt.Errorf("%w: a suggestion can only be decided enabled or declined", ErrAgentTransition)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return AgentSuggestion{}, err
	}
	defer tx.Rollback()
	cur, err := scanSuggestion(tx.QueryRowContext(ctx, `SELECT `+suggestionCols+` FROM agent_suggestions WHERE id=?`, id))
	if err != nil {
		return AgentSuggestion{}, err
	}
	if cur.State == state {
		return cur, nil
	}
	if cur.State != SuggestionPending {
		return AgentSuggestion{}, fmt.Errorf("%w: suggestion is %s", ErrAgentTransition, cur.State)
	}
	res, err := tx.ExecContext(ctx, `UPDATE agent_suggestions SET state=?,decided_at=? WHERE id=? AND state=?`, state, Now(), id, SuggestionPending)
	if err != nil {
		return AgentSuggestion{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return AgentSuggestion{}, err
	} else if n != 1 {
		return AgentSuggestion{}, ErrAgentConflict
	}
	out, err := scanSuggestion(tx.QueryRowContext(ctx, `SELECT `+suggestionCols+` FROM agent_suggestions WHERE id=?`, id))
	if err != nil {
		return AgentSuggestion{}, err
	}
	return out, tx.Commit()
}

// ExpirePendingAgentSuggestions marks every pending suggestion expired (an
// engine restart drops the live approval) and returns how many changed.
func (s *Store) ExpirePendingAgentSuggestions(ctx context.Context) (int, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE agent_suggestions SET state=?,decided_at=? WHERE state=?`, SuggestionExpired, Now(), SuggestionPending)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// LatestAgentSuggestion returns the thread's newest suggestion (ErrNotFound if none).
func (s *Store) LatestAgentSuggestion(ctx context.Context, threadID string) (AgentSuggestion, error) {
	return scanSuggestion(s.DB.QueryRowContext(ctx, `SELECT `+suggestionCols+` FROM agent_suggestions WHERE thread_id=? ORDER BY created_at DESC, rowid DESC LIMIT 1`, threadID))
}

// ThreadDeclinedSuggestion reports whether the thread has a declined
// suggestion that has not been cleared.
func (s *Store) ThreadDeclinedSuggestion(ctx context.Context, threadID string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM agent_suggestions WHERE thread_id=? AND state=? AND decline_cleared=0`, threadID, SuggestionDeclined).Scan(&n)
	return n > 0, err
}

// ClearThreadDeclines lifts suggestion suppression (the user enabled agents
// themselves). The declined records stay as history.
func (s *Store) ClearThreadDeclines(ctx context.Context, threadID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE agent_suggestions SET decline_cleared=1 WHERE thread_id=? AND state=? AND decline_cleared=0`, threadID, SuggestionDeclined)
	return err
}
