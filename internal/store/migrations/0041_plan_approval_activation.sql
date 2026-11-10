-- Rebuild the table to extend the original activation CHECK. Foreign keys are
-- disabled by the migration runner for this migration only, then checked before
-- commit and enabled again. All child rows retain their original parent IDs.
CREATE TABLE agent_tasks_new (
 id TEXT PRIMARY KEY,
 thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
 root_turn_id TEXT NOT NULL,
 activation TEXT NOT NULL CHECK (activation IN ('explicit_user','installed_skill','plan_approval')),
 state TEXT NOT NULL CHECK (state IN ('queued','running','waiting_approval','completed','failed','cancelled','interrupted')),
 base_revision TEXT NOT NULL,
 project_id TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 base_root TEXT NOT NULL DEFAULT '',
 parent_root TEXT NOT NULL DEFAULT '',
 FOREIGN KEY(root_turn_id, thread_id) REFERENCES turns(id, thread_id) ON DELETE CASCADE,
 UNIQUE(thread_id, root_turn_id)
);
INSERT INTO agent_tasks_new(id,thread_id,root_turn_id,activation,state,base_revision,project_id,created_at,updated_at,base_root,parent_root)
 SELECT id,thread_id,root_turn_id,activation,state,base_revision,project_id,created_at,updated_at,base_root,parent_root FROM agent_tasks;
DROP TABLE agent_tasks;
ALTER TABLE agent_tasks_new RENAME TO agent_tasks;
