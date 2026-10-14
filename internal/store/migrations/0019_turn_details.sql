CREATE TABLE turn_diagnostics (
 turn_id TEXT PRIMARY KEY REFERENCES turns(id) ON DELETE CASCADE,
 data_json TEXT NOT NULL
);
