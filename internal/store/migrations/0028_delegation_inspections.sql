-- Inspection snapshots are per submitted revision; implementers and bound
-- reviewers still have one owned workspace per child/kind.
CREATE TABLE agent_workspaces_v2 (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
 child_id TEXT NOT NULL REFERENCES agent_children(id) ON DELETE CASCADE,
 kind TEXT NOT NULL CHECK(kind IN ('implementer','reviewer','inspection')),
 data_json TEXT NOT NULL
);
INSERT INTO agent_workspaces_v2(id,task_id,child_id,kind,data_json)
 SELECT w.id,w.task_id,w.child_id,
 CASE WHEN w.kind='reviewer' AND c.role='implementer' THEN 'inspection' ELSE w.kind END,
 CASE WHEN w.kind='reviewer' AND c.role='implementer' THEN json_set(w.data_json,'$.Kind','inspection') ELSE w.data_json END
 FROM agent_workspaces w JOIN agent_children c ON c.id=w.child_id;
DROP TABLE agent_workspaces;
ALTER TABLE agent_workspaces_v2 RENAME TO agent_workspaces;
CREATE UNIQUE INDEX idx_agent_owned_workspaces ON agent_workspaces(child_id,kind) WHERE kind!='inspection';
