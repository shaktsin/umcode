CREATE TABLE IF NOT EXISTS plugin_component_health (
    plugin_id   TEXT NOT NULL REFERENCES plugin_installations(id) ON DELETE CASCADE,
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    component   TEXT NOT NULL,
    status      TEXT NOT NULL,
    error_text  TEXT NOT NULL DEFAULT '',
    updated_at  TEXT NOT NULL,
    PRIMARY KEY (plugin_id, project_id, component)
);
CREATE INDEX IF NOT EXISTS idx_plugin_component_health_project
    ON plugin_component_health(project_id, plugin_id);
