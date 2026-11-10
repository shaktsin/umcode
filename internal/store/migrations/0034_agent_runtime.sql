CREATE TABLE agents (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
 call_id TEXT NOT NULL,
 parent_agent_id TEXT NOT NULL DEFAULT '',
 name TEXT NOT NULL,
 purpose TEXT NOT NULL DEFAULT '',
 plan_id TEXT NOT NULL DEFAULT '',
 plan_entry TEXT NOT NULL DEFAULT '',
 assignment_json TEXT NOT NULL DEFAULT '{}',
 capabilities_json TEXT NOT NULL DEFAULT '{}',
 rejected_json TEXT NOT NULL DEFAULT '[]',
 model_requested TEXT NOT NULL DEFAULT '',
 model_actual TEXT NOT NULL DEFAULT '',
 route_trail_json TEXT NOT NULL DEFAULT '[]',
 budget_share REAL NOT NULL DEFAULT 0,
 depth INTEGER NOT NULL DEFAULT 0,
 context_mode TEXT NOT NULL DEFAULT '',
 workspace_id TEXT NOT NULL DEFAULT '',
 revision TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL,
 result_json TEXT NOT NULL DEFAULT '{}',
 usage_json TEXT NOT NULL DEFAULT '{}',
 version INTEGER NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(task_id, call_id)
);
-- Agents and messages are read in rowid (insertion) order.
CREATE INDEX agents_task ON agents(task_id);

CREATE TABLE agent_plans (
 id TEXT PRIMARY KEY,
 thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
 task_id TEXT NOT NULL DEFAULT '',
 agents_json TEXT NOT NULL DEFAULT '[]',
 limits_json TEXT NOT NULL DEFAULT '{}',
 state TEXT NOT NULL,
 created_at TEXT NOT NULL,
 approved_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX agent_plans_thread ON agent_plans(thread_id, created_at);

CREATE TABLE agent_messages (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
 from_agent_id TEXT NOT NULL,
 to_agent_id TEXT NOT NULL,
 body TEXT NOT NULL,
 call_id TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 delivered_at TEXT
);
CREATE INDEX agent_messages_to ON agent_messages(task_id, to_agent_id);
CREATE UNIQUE INDEX agent_messages_call ON agent_messages(task_id, call_id) WHERE call_id != '';

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
