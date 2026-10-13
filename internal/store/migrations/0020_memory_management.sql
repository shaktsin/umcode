CREATE TABLE memory_retirements (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 semantic_key TEXT NOT NULL,
 source_revision TEXT NOT NULL,
 text_hash TEXT NOT NULL,
 created_at TEXT NOT NULL,
 UNIQUE(project_id,semantic_key,source_revision,text_hash)
);
CREATE TABLE memory_management_ops (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 memory_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('correct','retire')),
 state TEXT NOT NULL CHECK(state IN ('prepared','file_written','committed','pending_repair','conflicted')),
 before_hash TEXT NOT NULL,
 after_hash TEXT NOT NULL DEFAULT '',
 before_bytes BLOB NOT NULL,
 after_bytes BLOB NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE INDEX idx_memory_management_ops_state ON memory_management_ops(state,created_at);
