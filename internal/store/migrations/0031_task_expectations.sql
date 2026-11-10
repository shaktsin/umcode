CREATE TABLE task_expectations (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL REFERENCES engine_tasks(id) ON DELETE CASCADE,
 origin_item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
 request_turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL CHECK(revision>=1),
 status TEXT NOT NULL CHECK(status IN ('pending','satisfied','superseded')),
 data_json TEXT NOT NULL CHECK(length(CAST(data_json AS BLOB))<=65536)
);
CREATE INDEX task_expectations_task ON task_expectations(task_id);
CREATE TABLE task_corrections (
 turn_id TEXT PRIMARY KEY REFERENCES turns(id) ON DELETE CASCADE,
 count INTEGER NOT NULL DEFAULT 0 CHECK(count>=0 AND count<=2)
);
