package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

const threadCols = `id, title, project_id, workspace_mode, channel, pinned, archived, provider, model, complexity, credential_id, approval_mode, forked_from, created_at, updated_at`

func scanThread(sc interface{ Scan(...any) error }) (protocol.Thread, error) {
	var t protocol.Thread
	var pinned, archived int
	var complexity, created, updated string
	err := sc.Scan(&t.ID, &t.Title, &t.ProjectID, &t.WorkspaceMode, &t.Channel, &pinned, &archived,
		&t.Settings.Provider, &t.Settings.Model, &complexity, &t.Settings.CredentialID,
		&t.ApprovalMode, &t.ForkedFrom, &created, &updated)
	if err != nil {
		return t, err
	}
	t.Pinned, t.Archived = pinned != 0, archived != 0
	t.Settings.Complexity = protocol.Complexity(complexity)
	t.CreatedAt, t.UpdatedAt = ParseTime(created), ParseTime(updated)
	return t, nil
}

// CreateThread inserts a new thread.
func (s *Store) CreateThread(ctx context.Context, t protocol.Thread) (protocol.Thread, error) {
	if t.ID == "" {
		t.ID = NewID("thr")
	}
	if t.Channel == "" {
		t.Channel = "app"
	}
	if t.WorkspaceMode == "" {
		t.WorkspaceMode = "local"
	}
	now := time.Now().UTC()
	t.CreatedAt, t.UpdatedAt = now, now
	_, err := s.DB.ExecContext(ctx, `INSERT INTO threads (`+threadCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Title, t.ProjectID, t.WorkspaceMode, t.Channel, b2i(t.Pinned), b2i(t.Archived),
		t.Settings.Provider, t.Settings.Model, string(t.Settings.Complexity), t.Settings.CredentialID,
		t.ApprovalMode, t.ForkedFrom, FormatTime(now), FormatTime(now))
	return t, err
}

// GetThread loads a thread with its usage totals.
func (s *Store) GetThread(ctx context.Context, id string) (protocol.Thread, error) {
	t, err := scanThread(s.DB.QueryRowContext(ctx, `SELECT `+threadCols+` FROM threads WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	t.Usage, err = s.UsageTotalsWhere(ctx, "thread_id = ?", id)
	return t, err
}

// ListThreads returns threads, pinned first then most recently updated.
func (s *Store) ListThreads(ctx context.Context, p protocol.ThreadListParams) ([]protocol.Thread, string, error) {
	limit := p.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var where []string
	var args []any
	archived := false
	if p.Archived != nil {
		archived = *p.Archived
	}
	where = append(where, "archived = ?")
	args = append(args, b2i(archived))
	if p.Channel != "" {
		where = append(where, "channel = ?")
		args = append(args, p.Channel)
	}
	if p.ProjectID != "" {
		where = append(where, "project_id = ?")
		args = append(args, p.ProjectID)
	}
	if p.Before != "" {
		where = append(where, "updated_at < ? AND pinned = 0")
		args = append(args, p.Before)
	}
	q := `SELECT ` + threadCols + ` FROM threads WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY pinned DESC, updated_at DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []protocol.Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = FormatTime(out[len(out)-1].UpdatedAt)
	}
	// Usage totals per thread in one query.
	if len(out) > 0 {
		ids := make([]any, len(out))
		ph := make([]string, len(out))
		for i, t := range out {
			ids[i], ph[i] = t.ID, "?"
		}
		totals, err := s.usageByThread(ctx, ph, ids)
		if err != nil {
			return nil, "", err
		}
		for i := range out {
			out[i].Usage = totals[out[i].ID]
		}
	}
	return out, next, nil
}

// UpdateThread applies a mutation to selected columns.
func (s *Store) UpdateThread(ctx context.Context, id string, cols map[string]any) error {
	allowed := map[string]bool{"title": true, "pinned": true, "archived": true, "provider": true,
		"model": true, "complexity": true, "credential_id": true, "workspace_mode": true, "approval_mode": true}
	var sets []string
	var args []any
	for k, v := range cols {
		if !allowed[k] {
			return fmt.Errorf("store: column %s not updatable", k)
		}
		if b, ok := v.(bool); ok {
			v = b2i(b)
		}
		sets = append(sets, k+" = ?")
		args = append(args, v)
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, Now(), id)
	res, err := s.DB.ExecContext(ctx, `UPDATE threads SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchThread bumps updated_at.
func (s *Store) TouchThread(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE threads SET updated_at = ? WHERE id = ?`, Now(), id)
	return err
}

// RememberThreadDecision stores an exact "always allow/deny" approval for one chat.
func (s *Store) RememberThreadDecision(ctx context.Context, threadID, tool, signature, decision string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO thread_approvals (thread_id, tool, signature, decision, created_at)
		VALUES (?,?,?,?,?) ON CONFLICT(thread_id, tool, signature) DO UPDATE SET decision = excluded.decision`,
		threadID, tool, signature, decision, FormatTime(time.Now().UTC()))
	return err
}

// RememberedThreadDecision returns "allow", "deny" or "" for an exact chat/tool call.
func (s *Store) RememberedThreadDecision(ctx context.Context, threadID, tool, signature string) string {
	var d string
	err := s.DB.QueryRowContext(ctx, `SELECT decision FROM thread_approvals
		WHERE thread_id = ? AND tool = ? AND signature = ?`, threadID, tool, signature).Scan(&d)
	if err != nil {
		return ""
	}
	return d
}

// DeleteThread removes a thread, its turns, items and search entries.
func (s *Store) DeleteThread(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM items_fts WHERE thread_id = ?`,
		`DELETE FROM items WHERE thread_id = ?`,
		`DELETE FROM turns WHERE thread_id = ?`,
		`DELETE FROM thread_approvals WHERE thread_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM threads WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// ---- turns ----

// CreateTurn inserts a running turn.
func (s *Store) CreateTurn(ctx context.Context, t protocol.Turn) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO turns (id, thread_id, status,
		sel_provider, sel_model, sel_complexity, sel_credential,
		res_provider, res_model, res_complexity, res_credential, auto_picked, error, started_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.ThreadID, t.Status,
		t.Selection.Provider, t.Selection.Model, string(t.Selection.Complexity), t.Selection.CredentialID,
		t.Resolved.Provider, t.Resolved.Model, string(t.Resolved.Complexity), t.Resolved.CredentialID,
		b2i(t.AutoPicked), t.Error, FormatTime(t.StartedAt))
	return err
}

// FinishTurn records the final status of a turn.
func (s *Store) FinishTurn(ctx context.Context, t protocol.Turn) error {
	fin := ""
	if t.FinishedAt != nil {
		fin = FormatTime(*t.FinishedAt)
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE turns SET status = ?, error = ?, finished_at = ?,
		res_provider = ?, res_model = ?, res_complexity = ?, res_credential = ?, auto_picked = ?
		WHERE id = ?`,
		t.Status, t.Error, fin, t.Resolved.Provider, t.Resolved.Model, string(t.Resolved.Complexity),
		t.Resolved.CredentialID, b2i(t.AutoPicked), t.ID)
	return err
}

// ListTurns returns a thread's turns in order, with usage.
func (s *Store) ListTurns(ctx context.Context, threadID string) ([]protocol.Turn, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, thread_id, status,
		sel_provider, sel_model, sel_complexity, sel_credential,
		res_provider, res_model, res_complexity, res_credential, auto_picked, error, started_at, finished_at,
		route_trail
		FROM turns WHERE thread_id = ? ORDER BY started_at, id`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Turn
	for rows.Next() {
		var t protocol.Turn
		var selC, resC, started, trail string
		var auto int
		var fin sql.NullString
		if err := rows.Scan(&t.ID, &t.ThreadID, &t.Status,
			&t.Selection.Provider, &t.Selection.Model, &selC, &t.Selection.CredentialID,
			&t.Resolved.Provider, &t.Resolved.Model, &resC, &t.Resolved.CredentialID,
			&auto, &t.Error, &started, &fin, &trail); err != nil {
			return nil, err
		}
		if trail != "" {
			_ = json.Unmarshal([]byte(trail), &t.RouteTrail)
		}
		t.Selection.Complexity, t.Resolved.Complexity = protocol.Complexity(selC), protocol.Complexity(resC)
		t.AutoPicked = auto != 0
		t.StartedAt, t.FinishedAt = ParseTime(started), nullTime(fin)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	usage, err := s.usageByTurn(ctx, threadID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Usage = usage[out[i].ID]
	}
	return out, nil
}

// MarkStaleTurns fails turns left running by a crashed engine.
func (s *Store) MarkStaleTurns(ctx context.Context) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE turns SET status = ?, error = 'engine restarted', finished_at = ?
		WHERE status = ?`, protocol.TurnInterrupted, Now(), protocol.TurnRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- items ----

// NextSeq returns the next item sequence number for a thread.
func (s *Store) NextSeq(ctx context.Context, threadID string) (int64, error) {
	var seq sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT MAX(seq) FROM items WHERE thread_id = ?`, threadID).Scan(&seq)
	return seq.Int64 + 1, err
}

// SaveItem inserts or replaces an item and refreshes its search entry when completed.
func (s *Store) SaveItem(ctx context.Context, it protocol.Item) error {
	toolJSON := ""
	if it.Tool != nil {
		b, err := json.Marshal(it.Tool)
		if err != nil {
			return err
		}
		toolJSON = string(b)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO items (id, thread_id, turn_id, seq, kind, status, text, tool_json, data_json, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status = excluded.status, text = excluded.text,
			tool_json = excluded.tool_json, data_json = excluded.data_json`,
		it.ID, it.ThreadID, it.TurnID, it.Seq, it.Kind, it.Status, it.Text, toolJSON, string(it.Data),
		FormatTime(it.CreatedAt))
	if err != nil {
		return err
	}
	if it.Status != protocol.ItemInProgress {
		if _, err := tx.ExecContext(ctx, `DELETE FROM items_fts WHERE item_id = ?`, it.ID); err != nil {
			return err
		}
		text := searchText(it)
		if text != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO items_fts (item_id, thread_id, text) VALUES (?,?,?)`,
				it.ID, it.ThreadID, text); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func searchText(it protocol.Item) string {
	switch it.Kind {
	case protocol.ItemUserMessage, protocol.ItemAgentMessage, protocol.ItemInboundEvent:
		return it.Text
	case protocol.ItemToolCall:
		if it.Tool != nil {
			return it.Tool.Name + " " + string(it.Tool.Args) + " " + truncate(it.Tool.Output, 4000)
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

const itemCols = `id, thread_id, turn_id, seq, kind, status, text, tool_json, data_json, created_at`

func scanItem(sc interface{ Scan(...any) error }) (protocol.Item, error) {
	var it protocol.Item
	var toolJSON, dataJSON, created string
	if err := sc.Scan(&it.ID, &it.ThreadID, &it.TurnID, &it.Seq, &it.Kind, &it.Status, &it.Text,
		&toolJSON, &dataJSON, &created); err != nil {
		return it, err
	}
	if toolJSON != "" {
		it.Tool = &protocol.ToolCallData{}
		if err := json.Unmarshal([]byte(toolJSON), it.Tool); err != nil {
			return it, err
		}
	}
	if dataJSON != "" {
		it.Data = json.RawMessage(dataJSON)
	}
	it.CreatedAt = ParseTime(created)
	return it, nil
}

// ListItems returns a thread's items in order. If upToSeq > 0 only items with seq <= upToSeq.
func (s *Store) ListItems(ctx context.Context, threadID string, upToSeq int64) ([]protocol.Item, error) {
	q := `SELECT ` + itemCols + ` FROM items WHERE thread_id = ?`
	args := []any{threadID}
	if upToSeq > 0 {
		q += ` AND seq <= ?`
		args = append(args, upToSeq)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// GetItem loads one item.
func (s *Store) GetItem(ctx context.Context, id string) (protocol.Item, error) {
	it, err := scanItem(s.DB.QueryRowContext(ctx, `SELECT `+itemCols+` FROM items WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return it, ErrNotFound
	}
	return it, err
}

// SearchItems runs a full-text query across all threads.
func (s *Store) SearchItems(ctx context.Context, query string, limit int) ([]protocol.SearchHit, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT f.item_id, f.thread_id, t.title,
			snippet(items_fts, 2, '[', ']', '…', 12)
		FROM items_fts f JOIN threads t ON t.id = f.thread_id
		WHERE items_fts MATCH ? ORDER BY rank LIMIT ?`, ftsQuery(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.SearchHit
	for rows.Next() {
		var h protocol.SearchHit
		if err := rows.Scan(&h.ItemID, &h.ThreadID, &h.Title, &h.Snippet); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ftsQuery turns free text into a safe FTS5 query: each word quoted, prefix-matched.
func ftsQuery(q string) string {
	var parts []string
	for _, w := range strings.Fields(q) {
		w = strings.ReplaceAll(w, `"`, `""`)
		parts = append(parts, `"`+w+`"*`)
	}
	if len(parts) == 0 {
		return `""`
	}
	return strings.Join(parts, " ")
}
