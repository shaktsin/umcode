CREATE TABLE agent_review_reports (
 reviewer_id TEXT PRIMARY KEY REFERENCES agent_children(id) ON DELETE CASCADE,
 task_id TEXT NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
 data_json TEXT NOT NULL
);
CREATE TABLE agent_workflow (
 task_id TEXT PRIMARY KEY REFERENCES agent_tasks(id) ON DELETE CASCADE,
 data_json TEXT NOT NULL
);
