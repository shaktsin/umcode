CREATE TABLE engine_tasks (
 id TEXT PRIMARY KEY,
 thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
 work_id TEXT REFERENCES works(id) ON DELETE SET NULL,
 origin_item_id TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
 current INTEGER NOT NULL DEFAULT 1 CHECK(current IN (0,1)),
 revision INTEGER NOT NULL CHECK(revision >= 1),
 status TEXT NOT NULL,
 data_json TEXT NOT NULL CHECK(length(CAST(data_json AS BLOB)) <= 65536)
);
CREATE UNIQUE INDEX engine_tasks_current ON engine_tasks(thread_id) WHERE current=1;
CREATE TABLE task_operations (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL REFERENCES engine_tasks(id) ON DELETE CASCADE,
 turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
 item_id TEXT REFERENCES items(id) ON DELETE SET NULL,
 revision INTEGER NOT NULL CHECK(revision >= 1),
 status TEXT NOT NULL,
 data_json TEXT NOT NULL CHECK(length(CAST(data_json AS BLOB)) <= 65536)
);
CREATE INDEX task_operations_task ON task_operations(task_id);
CREATE TABLE task_requirements (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL REFERENCES engine_tasks(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL CHECK(revision >= 1),
 data_json TEXT NOT NULL CHECK(length(CAST(data_json AS BLOB)) <= 65536)
);
