package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

const projectCols = `id, name, root, instructions_path, provider, model, complexity, credential_id,
	tools, archived, created_at, last_opened_at`

func scanProject(sc interface{ Scan(...any) error }) (protocol.Project, error) {
	var p protocol.Project
	var complexity, toolsJSON, created, opened string
	var archived int
	err := sc.Scan(&p.ID, &p.Name, &p.Root, &p.InstructionsPath,
		&p.Settings.Provider, &p.Settings.Model, &complexity, &p.Settings.CredentialID,
		&toolsJSON, &archived, &created, &opened)
	if err != nil {
		return p, err
	}
	p.Settings.Complexity = protocol.Complexity(complexity)
	p.Archived = archived != 0
	if toolsJSON != "" {
		_ = json.Unmarshal([]byte(toolsJSON), &p.Tools)
	}
	p.CreatedAt, p.LastOpenedAt = ParseTime(created), ParseTime(opened)
	return p, nil
}

// CreateProject inserts a project. Root must already be absolute and clean.
func (s *Store) CreateProject(ctx context.Context, p protocol.Project) (protocol.Project, error) {
	if p.ID == "" {
		p.ID = NewID("prj")
	}
	now := time.Now().UTC()
	p.CreatedAt, p.LastOpenedAt = now, now
	tools, err := json.Marshal(p.Tools)
	if err != nil {
		return p, err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO projects (`+projectCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Name, p.Root, p.InstructionsPath,
		p.Settings.Provider, p.Settings.Model, string(p.Settings.Complexity), p.Settings.CredentialID,
		string(tools), b2i(p.Archived), FormatTime(now), FormatTime(now))
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return p, ErrDuplicate
	}
	return p, err
}

// ErrDuplicate is returned when a project already exists for a folder.
var ErrDuplicate = errors.New("already exists")

// GetProject loads one project, with its thread count.
func (s *Store) GetProject(ctx context.Context, id string) (protocol.Project, error) {
	p, err := scanProject(s.DB.QueryRowContext(ctx, `SELECT `+projectCols+` FROM projects WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM threads WHERE project_id = ?`, id).Scan(&p.Threads)
	return p, nil
}

// ProjectByRoot finds a project by its folder.
func (s *Store) ProjectByRoot(ctx context.Context, root string) (protocol.Project, error) {
	p, err := scanProject(s.DB.QueryRowContext(ctx, `SELECT `+projectCols+` FROM projects WHERE root = ?`, root))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// ListProjects returns projects, most recently opened first.
func (s *Store) ListProjects(ctx context.Context, includeArchived bool) ([]protocol.Project, error) {
	// Counts first: the driver keeps one connection, so a second query while
	// rows are open would deadlock.
	counts := map[string]int{}
	crows, err := s.DB.QueryContext(ctx, `SELECT project_id, COUNT(*) FROM threads WHERE project_id != '' GROUP BY project_id`)
	if err == nil {
		for crows.Next() {
			var id string
			var n int
			if crows.Scan(&id, &n) == nil {
				counts[id] = n
			}
		}
		crows.Close()
	}
	q := `SELECT ` + projectCols + ` FROM projects`
	if !includeArchived {
		q += ` WHERE archived = 0`
	}
	q += ` ORDER BY last_opened_at DESC`
	rows, err := s.DB.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		p.Threads = counts[p.ID]
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProject applies the non-nil fields of p.
func (s *Store) UpdateProject(ctx context.Context, p protocol.ProjectUpdateParams) (protocol.Project, error) {
	cur, err := s.GetProject(ctx, p.ProjectID)
	if err != nil {
		return cur, err
	}
	if p.Name != nil {
		cur.Name = strings.TrimSpace(*p.Name)
	}
	if p.Root != nil {
		if cur.Root != *p.Root {
			cur.Root = *p.Root
			// A custom instructions path belongs to the old folder. Let the new
			// project root resolve its own instructions by default.
			cur.InstructionsPath = ""
		}
	}
	if p.Settings != nil {
		cur.Settings = *p.Settings
	}
	if p.Tools != nil {
		cur.Tools = *p.Tools
	}
	if p.InstructionsPath != nil {
		cur.InstructionsPath = *p.InstructionsPath
	}
	if p.Archived != nil {
		cur.Archived = *p.Archived
	}
	tools, err := json.Marshal(cur.Tools)
	if err != nil {
		return cur, err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE projects SET name = ?, root = ?, instructions_path = ?, provider = ?, model = ?,
		complexity = ?, credential_id = ?, tools = ?, archived = ? WHERE id = ?`,
		cur.Name, cur.Root, cur.InstructionsPath, cur.Settings.Provider, cur.Settings.Model,
		string(cur.Settings.Complexity), cur.Settings.CredentialID, string(tools), b2i(cur.Archived), cur.ID)
	return cur, err
}

// TouchProject records that a project was opened.
func (s *Store) TouchProject(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE projects SET last_opened_at = ? WHERE id = ?`, FormatTime(time.Now().UTC()), id)
	return err
}

// DeleteProject removes the project row and detaches its chats. The folder on
// disk is never touched.
func (s *Store) DeleteProject(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`UPDATE threads SET project_id = '' WHERE project_id = ?`,
		`DELETE FROM project_approvals WHERE project_id = ?`,
		`DELETE FROM file_changes WHERE project_id = ?`,
		`DELETE FROM project_plugins WHERE project_id = ?`,
		`DELETE FROM projects WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- file changes ----

// FileChange is one recorded edit, with the previous content when it was small
// enough to keep for an undo.
type FileChange struct {
	ID         int64
	ProjectID  string
	ThreadID   string
	TurnID     string
	ItemID     string
	Path       string
	Action     string
	Before     *string
	AfterHash  string
	Additions  int
	Deletions  int
	Revertable bool
	CreatedAt  time.Time
}

// RecordFileChange stores one edit.
func (s *Store) RecordFileChange(ctx context.Context, c FileChange) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO file_changes
		(project_id, thread_id, turn_id, item_id, path, action, before_blob, after_hash, additions, deletions, revertable, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ProjectID, c.ThreadID, c.TurnID, c.ItemID, c.Path, c.Action, c.Before, c.AfterHash,
		c.Additions, c.Deletions, b2i(c.Revertable), FormatTime(time.Now().UTC()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListFileChanges returns recorded edits, newest first. turnID or projectID may
// be empty to widen the query.
func (s *Store) ListFileChanges(ctx context.Context, projectID, turnID, path string, limit int) ([]FileChange, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var where []string
	var args []any
	if projectID != "" {
		where = append(where, "project_id = ?")
		args = append(args, projectID)
	}
	if turnID != "" {
		where = append(where, "turn_id = ?")
		args = append(args, turnID)
	}
	if path != "" {
		where = append(where, "path = ?")
		args = append(args, path)
	}
	q := `SELECT id, project_id, thread_id, turn_id, item_id, path, action, before_blob,
		additions, deletions, revertable, created_at FROM file_changes`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileChange
	for rows.Next() {
		var c FileChange
		var before sql.NullString
		var revertable int
		var created string
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.ThreadID, &c.TurnID, &c.ItemID, &c.Path, &c.Action,
			&before, &c.Additions, &c.Deletions, &revertable, &created); err != nil {
			return nil, err
		}
		if before.Valid {
			v := before.String
			c.Before = &v
		}
		c.Revertable = revertable != 0
		c.CreatedAt = ParseTime(created)
		out = append(out, c)
	}
	return out, rows.Err()
}
