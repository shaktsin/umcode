CREATE TABLE agent_followups (
 task_id TEXT NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
 child_id TEXT NOT NULL REFERENCES agent_children(id) ON DELETE CASCADE,
 call_id TEXT NOT NULL CHECK(length(call_id)>0),
 message TEXT NOT NULL CHECK(length(message)>0),
 round INTEGER NOT NULL CHECK(round BETWEEN 1 AND 2),
 delivered INTEGER NOT NULL DEFAULT 0 CHECK(delivered IN (0,1)),
 created_at TEXT NOT NULL,
 PRIMARY KEY(task_id,child_id,call_id),
 UNIQUE(child_id,round)
);
