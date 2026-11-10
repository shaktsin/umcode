CREATE TABLE IF NOT EXISTS agent_plan_sources (
 plan_id TEXT PRIMARY KEY REFERENCES agent_plans(id) ON DELETE CASCADE,
 source_root TEXT NOT NULL,
 manifest_json TEXT NOT NULL,
 created_at TEXT NOT NULL
);
