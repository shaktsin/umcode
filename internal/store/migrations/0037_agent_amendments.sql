ALTER TABLE agent_integration_head ADD COLUMN amendments INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_integration_head ADD COLUMN amend_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_integration_head ADD COLUMN amended_tree TEXT NOT NULL DEFAULT '';
