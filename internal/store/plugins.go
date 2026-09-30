package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type PluginInstallation struct {
	ID              string
	Name            string
	Version         string
	Format          string
	Source          string
	SourceID        string
	Mode            string
	Root            string
	Digest          string
	DiagnosticsJSON string
	Active          bool
	InstalledAt     time.Time
	UpdatedAt       time.Time
}

type ProjectPlugin struct {
	ProjectID string
	PluginID  string
	Enabled   bool
	Settings  map[string]any
	UpdatedAt time.Time
}

type PluginHookRun struct {
	ID            int64
	PluginID      string
	PluginVersion string
	ProjectID     string
	ThreadID      string
	TurnID        string
	Event         string
	Status        string
	Blocked       bool
	Reason        string
	Warning       string
	Context       string
	Error         string
	DurationMS    int64
	CreatedAt     time.Time
}

const pluginInstallationCols = `id, name, version, format, source, source_id, mode, root, digest,
	diagnostics_json, active, installed_at, updated_at`

func scanPluginInstallation(sc interface{ Scan(...any) error }) (PluginInstallation, error) {
	var p PluginInstallation
	var active int
	var installedAt, updatedAt string
	err := sc.Scan(&p.ID, &p.Name, &p.Version, &p.Format, &p.Source, &p.SourceID, &p.Mode,
		&p.Root, &p.Digest, &p.DiagnosticsJSON, &active, &installedAt, &updatedAt)
	p.Active = active != 0
	p.InstalledAt = ParseTime(installedAt)
	p.UpdatedAt = ParseTime(updatedAt)
	return p, err
}

func (s *Store) UpsertPluginInstallation(ctx context.Context, p PluginInstallation) error {
	now := time.Now().UTC()
	if p.InstalledAt.IsZero() {
		p.InstalledAt = now
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = now
	}
	if p.DiagnosticsJSON == "" {
		p.DiagnosticsJSON = "[]"
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO plugin_installations (`+pluginInstallationCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, version=excluded.version, format=excluded.format,
		source=excluded.source, source_id=excluded.source_id, mode=excluded.mode, root=excluded.root,
		digest=excluded.digest, diagnostics_json=excluded.diagnostics_json, active=excluded.active,
		updated_at=excluded.updated_at`,
		p.ID, p.Name, p.Version, p.Format, p.Source, p.SourceID, p.Mode, p.Root, p.Digest,
		p.DiagnosticsJSON, b2i(p.Active), FormatTime(p.InstalledAt), FormatTime(p.UpdatedAt))
	return err
}

func (s *Store) GetPluginInstallation(ctx context.Context, id string) (PluginInstallation, error) {
	p, err := scanPluginInstallation(s.DB.QueryRowContext(ctx, `SELECT `+pluginInstallationCols+` FROM plugin_installations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) ListPluginInstallations(ctx context.Context) ([]PluginInstallation, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+pluginInstallationCols+` FROM plugin_installations ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plugins []PluginInstallation
	for rows.Next() {
		p, err := scanPluginInstallation(rows)
		if err != nil {
			return nil, err
		}
		plugins = append(plugins, p)
	}
	return plugins, rows.Err()
}

func (s *Store) DeletePluginInstallation(ctx context.Context, id string) error {
	result, err := s.DB.ExecContext(ctx, `DELETE FROM plugin_installations WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetProjectPlugin(ctx context.Context, p ProjectPlugin) error {
	settings, err := json.Marshal(p.Settings)
	if err != nil {
		return err
	}
	if p.Settings == nil {
		settings = []byte("{}")
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now().UTC()
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO project_plugins (project_id, plugin_id, enabled, settings_json, updated_at)
		VALUES (?,?,?,?,?) ON CONFLICT(project_id, plugin_id) DO UPDATE SET
		enabled=excluded.enabled, settings_json=excluded.settings_json, updated_at=excluded.updated_at`,
		p.ProjectID, p.PluginID, b2i(p.Enabled), string(settings), FormatTime(p.UpdatedAt))
	return err
}

func scanProjectPlugin(sc interface{ Scan(...any) error }) (ProjectPlugin, error) {
	var p ProjectPlugin
	var enabled int
	var settings, updatedAt string
	err := sc.Scan(&p.ProjectID, &p.PluginID, &enabled, &settings, &updatedAt)
	p.Enabled = enabled != 0
	p.UpdatedAt = ParseTime(updatedAt)
	if err == nil && settings != "" {
		err = json.Unmarshal([]byte(settings), &p.Settings)
	}
	if p.Settings == nil {
		p.Settings = map[string]any{}
	}
	return p, err
}

func (s *Store) ProjectPlugin(ctx context.Context, projectID, pluginID string) (ProjectPlugin, error) {
	p, err := scanProjectPlugin(s.DB.QueryRowContext(ctx, `SELECT project_id, plugin_id, enabled, settings_json, updated_at
		FROM project_plugins WHERE project_id = ? AND plugin_id = ?`, projectID, pluginID))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) ListProjectPlugins(ctx context.Context, projectID string) ([]ProjectPlugin, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT project_id, plugin_id, enabled, settings_json, updated_at
		FROM project_plugins WHERE project_id = ? ORDER BY plugin_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plugins []ProjectPlugin
	for rows.Next() {
		p, err := scanProjectPlugin(rows)
		if err != nil {
			return nil, err
		}
		plugins = append(plugins, p)
	}
	return plugins, rows.Err()
}

func (s *Store) ProjectsUsingPlugin(ctx context.Context, pluginID string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT project_id FROM project_plugins WHERE plugin_id = ? AND enabled = 1 ORDER BY project_id`, pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projectIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		projectIDs = append(projectIDs, id)
	}
	return projectIDs, rows.Err()
}

func (s *Store) RecordPluginHookRun(ctx context.Context, run PluginHookRun) error {
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO plugin_hook_runs
		(plugin_id, plugin_version, project_id, thread_id, turn_id, event, status, blocked,
		reason, warning, context_text, error_text, duration_ms, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, run.PluginID, run.PluginVersion, run.ProjectID,
		run.ThreadID, run.TurnID, run.Event, run.Status, b2i(run.Blocked), run.Reason, run.Warning,
		run.Context, run.Error, run.DurationMS, FormatTime(run.CreatedAt))
	return err
}

func (s *Store) ListPluginHookRuns(ctx context.Context, pluginID, projectID, turnID string, limit int) ([]PluginHookRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var where []string
	var args []any
	for column, value := range map[string]string{"plugin_id": pluginID, "project_id": projectID, "turn_id": turnID} {
		if value != "" {
			where = append(where, column+" = ?")
			args = append(args, value)
		}
	}
	q := `SELECT id, plugin_id, plugin_version, project_id, thread_id, turn_id, event, status,
		blocked, reason, warning, context_text, error_text, duration_ms, created_at FROM plugin_hook_runs`
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
	var runs []PluginHookRun
	for rows.Next() {
		var run PluginHookRun
		var blocked int
		var createdAt string
		if err := rows.Scan(&run.ID, &run.PluginID, &run.PluginVersion, &run.ProjectID, &run.ThreadID,
			&run.TurnID, &run.Event, &run.Status, &blocked, &run.Reason, &run.Warning, &run.Context,
			&run.Error, &run.DurationMS, &createdAt); err != nil {
			return nil, err
		}
		run.Blocked = blocked != 0
		run.CreatedAt = ParseTime(createdAt)
		runs = append(runs, run)
	}
	return runs, rows.Err()
}
