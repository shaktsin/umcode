CREATE TABLE IF NOT EXISTS agent_task_merge_journal (
 task_id TEXT PRIMARY KEY REFERENCES agent_tasks(id) ON DELETE CASCADE,
 source_root TEXT NOT NULL,
 head_tree TEXT NOT NULL,
 plan_json TEXT NOT NULL,
 receipt_json TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL CHECK(state IN ('intent','conflict','merged','ambiguous')),
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
