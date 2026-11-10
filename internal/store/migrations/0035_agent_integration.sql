CREATE TABLE agent_revisions (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL,
 workspace_id TEXT NOT NULL DEFAULT '',
 base_revision TEXT NOT NULL,
 result_revision TEXT NOT NULL,
 result_tree TEXT NOT NULL DEFAULT '',
 seq INTEGER NOT NULL,
 state TEXT NOT NULL,
 reason TEXT NOT NULL DEFAULT '',
 changed_paths_json TEXT NOT NULL DEFAULT '[]',
 tree_before TEXT NOT NULL DEFAULT '',
 tree_after TEXT NOT NULL DEFAULT '',
 conflict_json TEXT NOT NULL DEFAULT '[]',
 version INTEGER NOT NULL DEFAULT 1,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(task_id, agent_id, result_revision)
);
CREATE INDEX agent_revisions_task ON agent_revisions(task_id, seq);

CREATE TABLE agent_integration_head (
 task_id TEXT PRIMARY KEY REFERENCES agent_tasks(id) ON DELETE CASCADE,
 head_tree TEXT NOT NULL,
 applied_count INTEGER NOT NULL DEFAULT 0,
 workflow_json TEXT NOT NULL DEFAULT '{}',
 version INTEGER NOT NULL DEFAULT 1,
 updated_at TEXT NOT NULL
);
