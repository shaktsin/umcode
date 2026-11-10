-- Durable delegation records are separate from legacy scheduled tasks/teams.
-- Root ownership is enforced even for writes outside the store methods.
CREATE UNIQUE INDEX idx_turns_delegation_owner ON turns(id, thread_id);
CREATE TABLE agent_tasks (
 id TEXT PRIMARY KEY,
 thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
 root_turn_id TEXT NOT NULL,
 activation TEXT NOT NULL CHECK (activation IN ('explicit_user','installed_skill')),
 state TEXT NOT NULL CHECK (state IN ('queued','running','waiting_approval','completed','failed','cancelled','interrupted')),
 base_revision TEXT NOT NULL,
 project_id TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 FOREIGN KEY(root_turn_id, thread_id) REFERENCES turns(id, thread_id) ON DELETE CASCADE,
 UNIQUE(thread_id, root_turn_id)
);
CREATE TABLE agent_children (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
 call_id TEXT NOT NULL CHECK (length(call_id) > 0),
 role TEXT NOT NULL CHECK (role IN ('implementer','reviewer')),
 assignment_json TEXT NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('queued','running','waiting_approval','completed','failed','cancelled','interrupted')),
 revision TEXT NOT NULL DEFAULT '',
 workspace_id TEXT NOT NULL DEFAULT '',
 result_json TEXT NOT NULL,
 usage_json TEXT NOT NULL,
 correction_rounds INTEGER NOT NULL DEFAULT 0 CHECK (correction_rounds BETWEEN 0 AND 2),
 version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(task_id, call_id)
);
CREATE INDEX idx_agent_children_task ON agent_children(task_id, created_at, id);
CREATE INDEX idx_agent_children_state ON agent_children(state);
