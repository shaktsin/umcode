CREATE TABLE memory_vault_refs (
 memory_id TEXT NOT NULL REFERENCES project_memories(id) ON DELETE CASCADE,
 vault_hash TEXT NOT NULL,
 PRIMARY KEY(memory_id,vault_hash)
);
CREATE INDEX idx_memory_vault_refs_hash ON memory_vault_refs(vault_hash);
INSERT OR IGNORE INTO memory_vault_refs(memory_id,vault_hash)
SELECT m.id,e.vault_hash FROM project_memories m,
 json_each(CASE WHEN json_valid(m.evidence_json) THEN m.evidence_json ELSE '[]' END) ids
 JOIN evidence e ON e.id=ids.value
WHERE m.status IN ('active','pending_repair') AND e.vault_hash<>'';
