CREATE TABLE IF NOT EXISTS retrieval_documents (
 id TEXT PRIMARY KEY,
 source_kind TEXT NOT NULL,
 source_id TEXT NOT NULL,
 work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
 source_hash TEXT NOT NULL,
 body TEXT NOT NULL,
 UNIQUE(source_kind,source_id)
);
CREATE VIRTUAL TABLE IF NOT EXISTS retrieval_fts USING fts5(body, content='retrieval_documents', content_rowid='rowid', tokenize='porter unicode61');
CREATE TRIGGER IF NOT EXISTS retrieval_insert AFTER INSERT ON retrieval_documents BEGIN
 INSERT INTO retrieval_fts(rowid,body) VALUES(new.rowid,new.body);
END;
CREATE TRIGGER IF NOT EXISTS retrieval_delete AFTER DELETE ON retrieval_documents BEGIN
 INSERT INTO retrieval_fts(retrieval_fts,rowid,body) VALUES('delete',old.rowid,old.body);
END;
CREATE TRIGGER IF NOT EXISTS retrieval_update AFTER UPDATE ON retrieval_documents BEGIN
 INSERT INTO retrieval_fts(retrieval_fts,rowid,body) VALUES('delete',old.rowid,old.body);
 INSERT INTO retrieval_fts(rowid,body) VALUES(new.rowid,new.body);
END;
CREATE TABLE IF NOT EXISTS observed_excerpts (
 id TEXT PRIMARY KEY,
 work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
 evidence_id TEXT NOT NULL REFERENCES evidence(id) ON DELETE CASCADE,
 path TEXT NOT NULL,
 workspace_root_hash TEXT NOT NULL,
 content_hash TEXT NOT NULL,
 start_line INTEGER NOT NULL,
 end_line INTEGER NOT NULL,
 text TEXT NOT NULL,
 observed_at TEXT NOT NULL
);
CREATE TRIGGER IF NOT EXISTS retrieval_node_delete AFTER DELETE ON work_nodes BEGIN
 DELETE FROM retrieval_documents WHERE source_kind='node' AND source_id=old.id;
END;
CREATE TRIGGER IF NOT EXISTS retrieval_evidence_delete AFTER DELETE ON evidence BEGIN
 DELETE FROM retrieval_documents WHERE source_kind='evidence' AND source_id=old.id;
END;
CREATE TRIGGER IF NOT EXISTS retrieval_excerpt_delete AFTER DELETE ON observed_excerpts BEGIN
 DELETE FROM retrieval_documents WHERE source_kind='excerpt' AND source_id=old.id;
END;
CREATE INDEX IF NOT EXISTS observed_excerpts_work ON observed_excerpts(work_id);
