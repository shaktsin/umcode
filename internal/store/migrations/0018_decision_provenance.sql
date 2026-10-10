ALTER TABLE work_nodes ADD COLUMN decision_actor TEXT NOT NULL DEFAULT '';
UPDATE work_nodes SET decision_actor='user' WHERE kind='decision' AND EXISTS (SELECT 1 FROM approvals a WHERE a.kind='workflow' AND a.status='approved' AND a.node_id=work_nodes.id AND a.node_revision=work_nodes.revision-1);
