ALTER TABLE threads ADD COLUMN internal_child_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_children ADD COLUMN model_json TEXT NOT NULL DEFAULT '{}';
CREATE UNIQUE INDEX threads_internal_child ON threads(internal_child_id) WHERE internal_child_id != '';
CREATE TABLE agent_executions (
 child_id TEXT PRIMARY KEY REFERENCES agent_children(id) ON DELETE CASCADE,
 task_id TEXT NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
 thread_id TEXT NOT NULL UNIQUE REFERENCES threads(id) ON DELETE CASCADE,
 context_json TEXT NOT NULL,
 model_json TEXT NOT NULL DEFAULT '{}'
);
ALTER TABLE approvals ADD COLUMN agent_task_id TEXT REFERENCES agent_tasks(id);
ALTER TABLE approvals ADD COLUMN agent_child_id TEXT REFERENCES agent_children(id);
CREATE TABLE agent_model_scopes (
 task_id TEXT PRIMARY KEY REFERENCES agent_tasks(id) ON DELETE CASCADE,
 scope_json TEXT NOT NULL
);
