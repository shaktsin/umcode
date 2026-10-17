ALTER TABLE agent_tasks ADD COLUMN parent_root TEXT NOT NULL DEFAULT '';
CREATE TABLE agent_workspaces (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
 child_id TEXT NOT NULL REFERENCES agent_children(id) ON DELETE CASCADE,
 kind TEXT NOT NULL CHECK(kind IN ('implementer','reviewer')),
 data_json TEXT NOT NULL,
 UNIQUE(child_id,kind)
);
CREATE TABLE agent_review_sources (
 task_id TEXT NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
 reviewer_id TEXT PRIMARY KEY REFERENCES agent_children(id) ON DELETE CASCADE,
 implementer_id TEXT NOT NULL REFERENCES agent_children(id) ON DELETE CASCADE,
 revision TEXT NOT NULL
);
CREATE TABLE agent_integrations (
 task_id TEXT NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
 child_id TEXT PRIMARY KEY REFERENCES agent_children(id) ON DELETE CASCADE,
 data_json TEXT NOT NULL
);
