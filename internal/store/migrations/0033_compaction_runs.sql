CREATE TABLE compaction_runs (
 thread_id TEXT PRIMARY KEY REFERENCES threads(id) ON DELETE CASCADE,
 id TEXT NOT NULL,
 sequence INTEGER NOT NULL,
 status TEXT NOT NULL,
 started_order INTEGER NOT NULL,
 data_json BLOB NOT NULL
);
