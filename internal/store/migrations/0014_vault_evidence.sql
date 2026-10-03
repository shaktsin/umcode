ALTER TABLE evidence ADD COLUMN vault_hash      TEXT NOT NULL DEFAULT '';
ALTER TABLE evidence ADD COLUMN env_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE evidence ADD COLUMN availability    TEXT NOT NULL DEFAULT 'none';
ALTER TABLE verification_attempts ADD COLUMN fingerprint_id TEXT;

CREATE TABLE IF NOT EXISTS vault_objects (
    hash               TEXT PRIMARY KEY,
    size               INTEGER NOT NULL DEFAULT 0,
    original_size      INTEGER NOT NULL DEFAULT 0,
    class              TEXT NOT NULL DEFAULT 'plain',
    truncated          INTEGER NOT NULL DEFAULT 0,
    status             TEXT NOT NULL DEFAULT 'available',
    created_at         TEXT NOT NULL,
    last_referenced_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS work_fingerprints (
    id         TEXT PRIMARY KEY,
    work_id    TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    turn_id    TEXT NOT NULL DEFAULT '',
    kind       TEXT NOT NULL,
    value      TEXT NOT NULL,
    paths_json TEXT NOT NULL DEFAULT '[]',
    taken_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_work_fingerprints_work ON work_fingerprints(work_id, taken_at);
CREATE INDEX IF NOT EXISTS idx_evidence_vault_hash ON evidence(vault_hash) WHERE vault_hash != '';
