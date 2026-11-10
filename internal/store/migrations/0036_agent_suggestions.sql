CREATE TABLE agent_suggestions (
 id TEXT PRIMARY KEY,
 thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
 turn_id TEXT NOT NULL,
 reason TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL CHECK(state IN ('pending','enabled','declined','expired')),
 decline_cleared INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL,
 decided_at TEXT NOT NULL DEFAULT '',
 UNIQUE(thread_id, turn_id)
);
CREATE INDEX agent_suggestions_thread ON agent_suggestions(thread_id, created_at);
