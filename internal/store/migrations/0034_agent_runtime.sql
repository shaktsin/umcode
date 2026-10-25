CREATE TABLE agents (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
 call_id TEXT NOT NULL,
 parent_agent_id TEXT NOT NULL DEFAULT '',
 name TEXT NOT NULL,
 purpose TEXT NOT NULL DEFAULT '',
 plan_id TEXT NOT NULL DEFAULT '',
 assignment_json TEXT NOT NULL DEFAULT '{}',
 capabilities_json TEXT NOT NULL DEFAULT '{}',
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
CREATE INDEX agents_task ON agents(task_id, created_at);

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
 created_at TEXT NOT NULL,
 delivered_at TEXT
);
CREATE INDEX agent_messages_to ON agent_messages(task_id, to_agent_id, created_at);
