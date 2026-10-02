CREATE TABLE IF NOT EXISTS works (
    id             TEXT PRIMARY KEY,
    thread_id      TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    project_id     TEXT REFERENCES projects(id) ON DELETE SET NULL,
    kind           TEXT NOT NULL DEFAULT 'task',
    status         TEXT NOT NULL DEFAULT 'open',
    workflow_depth TEXT NOT NULL DEFAULT 'direct',
    goal           TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    completed_at   TEXT
);
CREATE INDEX IF NOT EXISTS idx_works_thread_status ON works(thread_id, status);

CREATE TABLE IF NOT EXISTS work_nodes (
    id            TEXT PRIMARY KEY,
    work_id       TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    kind          TEXT NOT NULL,
    title         TEXT NOT NULL DEFAULT '',
    content_json  TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT '',
    confidence    REAL NOT NULL DEFAULT 1,
    revision      INTEGER NOT NULL DEFAULT 1,
    valid_from    TEXT NOT NULL,
    valid_until   TEXT,
    superseded_by TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_work_nodes_work_kind ON work_nodes(work_id, kind);

CREATE TABLE IF NOT EXISTS work_edges (
    work_id      TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    from_node_id TEXT NOT NULL,
    relation     TEXT NOT NULL,
    to_node_id   TEXT NOT NULL,
    PRIMARY KEY (work_id, from_node_id, relation, to_node_id)
);
CREATE INDEX IF NOT EXISTS idx_work_edges_from ON work_edges(work_id, from_node_id);

CREATE TABLE IF NOT EXISTS evidence (
    id              TEXT PRIMARY KEY,
    work_id         TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    node_id         TEXT,
    kind            TEXT NOT NULL,
    source_uri      TEXT NOT NULL DEFAULT '',
    source_revision TEXT NOT NULL DEFAULT '',
    content_hash    TEXT NOT NULL DEFAULT '',
    summary         TEXT NOT NULL DEFAULT '',
    confidence      REAL NOT NULL DEFAULT 1,
    observed_at     TEXT NOT NULL,
    stale_at        TEXT
);
CREATE INDEX IF NOT EXISTS idx_evidence_work ON evidence(work_id);

CREATE TABLE IF NOT EXISTS verification_attempts (
    id                TEXT PRIMARY KEY,
    work_id           TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    criterion_node_id TEXT,
    check_type        TEXT NOT NULL,
    command           TEXT NOT NULL DEFAULT '',
    environment_json  TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL,
    exit_code         INTEGER,
    evidence_id       TEXT,
    started_at        TEXT NOT NULL,
    finished_at       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_verification_attempts_work
    ON verification_attempts(work_id, criterion_node_id, started_at);
