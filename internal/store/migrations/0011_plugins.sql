-- Plugins are installed once per user and enabled/configured per project.
CREATE TABLE IF NOT EXISTS plugin_installations (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    version          TEXT NOT NULL DEFAULT '',
    format           TEXT NOT NULL,
    source           TEXT NOT NULL,
    source_id        TEXT NOT NULL,
    mode             TEXT NOT NULL,
    root             TEXT NOT NULL,
    digest           TEXT NOT NULL,
    diagnostics_json TEXT NOT NULL DEFAULT '[]',
    active           INTEGER NOT NULL DEFAULT 1,
    installed_at     TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    UNIQUE(source_id, name)
);
CREATE INDEX IF NOT EXISTS idx_plugin_installations_name ON plugin_installations(name, id);

CREATE TABLE IF NOT EXISTS project_plugins (
    project_id   TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    plugin_id    TEXT NOT NULL REFERENCES plugin_installations(id) ON DELETE CASCADE,
    enabled      INTEGER NOT NULL DEFAULT 0,
    settings_json TEXT NOT NULL DEFAULT '{}',
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (project_id, plugin_id)
);
CREATE INDEX IF NOT EXISTS idx_project_plugins_enabled ON project_plugins(plugin_id, enabled, project_id);

-- Hook audit history intentionally has no deleting foreign keys: uninstalling
-- a plugin or deleting a project must not erase its execution record.
CREATE TABLE IF NOT EXISTS plugin_hook_runs (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    plugin_id      TEXT NOT NULL,
    plugin_version TEXT NOT NULL DEFAULT '',
    project_id     TEXT NOT NULL DEFAULT '',
    thread_id      TEXT NOT NULL DEFAULT '',
    turn_id        TEXT NOT NULL DEFAULT '',
    event          TEXT NOT NULL,
    status         TEXT NOT NULL,
    blocked        INTEGER NOT NULL DEFAULT 0,
    reason         TEXT NOT NULL DEFAULT '',
    warning        TEXT NOT NULL DEFAULT '',
    context_text   TEXT NOT NULL DEFAULT '',
    error_text     TEXT NOT NULL DEFAULT '',
    duration_ms    INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_plugin_hook_runs_lookup
    ON plugin_hook_runs(plugin_id, project_id, turn_id, id DESC);
