CREATE TABLE agent_task_workspaces (
 task_id TEXT PRIMARY KEY REFERENCES agent_tasks(id) ON DELETE CASCADE,
 source_root TEXT NOT NULL,
 execution_root TEXT NOT NULL,
 source_head TEXT NOT NULL,
 snapshot_commit TEXT NOT NULL,
 manifest_json TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('prepared','active','merged','conflict')),
 created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX agent_task_workspaces_execution_root ON agent_task_workspaces(execution_root);
