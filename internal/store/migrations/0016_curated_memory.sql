-- Source IDs are immutable provenance, deliberately without foreign keys:
-- deleting a source chat or Work must preserve memory and its audit trail.
CREATE TABLE project_memories (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    work_id TEXT NOT NULL,
    candidate_node_id TEXT NOT NULL,
    semantic_key TEXT NOT NULL,
    category TEXT NOT NULL,
    target_path TEXT NOT NULL,
    text TEXT NOT NULL,
    text_hash TEXT NOT NULL,
    status TEXT NOT NULL,
    source_revision TEXT NOT NULL DEFAULT '',
    evidence_json TEXT NOT NULL DEFAULT '[]',
    file_hash_before TEXT NOT NULL DEFAULT '',
    file_hash_after TEXT NOT NULL DEFAULT '',
    superseded_by TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    promoted_at TEXT
);
CREATE INDEX idx_project_memories_project_target ON project_memories(project_id, target_path, status);
CREATE UNIQUE INDEX idx_project_memories_active_semantic_key
    ON project_memories(project_id, semantic_key) WHERE status = 'active';

CREATE TABLE memory_promotion_ops (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    work_id TEXT NOT NULL,
    candidate_node_id TEXT NOT NULL,
    -- Preparation may precede insertion of the planned memory row.
    memory_id TEXT NOT NULL,
    thread_id TEXT NOT NULL,
    turn_id TEXT NOT NULL,
    target_path TEXT NOT NULL,
    state TEXT NOT NULL,
    file_hash_before TEXT NOT NULL DEFAULT '',
    file_hash_after TEXT NOT NULL DEFAULT '',
    before_bytes BLOB,
    after_bytes BLOB,
    error_class TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_memory_promotion_ops_project ON memory_promotion_ops(project_id, created_at);
CREATE INDEX idx_memory_promotion_ops_state ON memory_promotion_ops(state, created_at);

ALTER TABLE file_changes ADD COLUMN promotion_op_id TEXT;
CREATE UNIQUE INDEX idx_file_changes_promotion_op ON file_changes(promotion_op_id)
    WHERE promotion_op_id IS NOT NULL AND promotion_op_id <> '';
