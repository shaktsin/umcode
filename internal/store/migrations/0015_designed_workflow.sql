ALTER TABLE works ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE approvals ADD COLUMN kind TEXT NOT NULL DEFAULT 'tool';
ALTER TABLE approvals ADD COLUMN work_id TEXT NOT NULL DEFAULT '';
ALTER TABLE approvals ADD COLUMN node_id TEXT NOT NULL DEFAULT '';
ALTER TABLE approvals ADD COLUMN node_revision INTEGER NOT NULL DEFAULT 0;

CREATE TABLE work_node_evidence (
    work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES work_nodes(id) ON DELETE CASCADE,
    evidence_id TEXT NOT NULL REFERENCES evidence(id) ON DELETE CASCADE,
    PRIMARY KEY (work_id, node_id, evidence_id)
);
CREATE INDEX idx_work_node_evidence_work_node ON work_node_evidence(work_id, node_id);
CREATE INDEX idx_approvals_work_node_status ON approvals(work_id, node_id, status);
