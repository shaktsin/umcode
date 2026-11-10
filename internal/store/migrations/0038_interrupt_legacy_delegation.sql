-- The old worker scheduler is gone. Preserve its rows for read-only history,
-- but never leave unfinished legacy work appearing runnable after upgrade.
-- Runtime mirror children are owned by agents and reconciled separately.
UPDATE agent_children
SET state = 'interrupted', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE state IN ('queued', 'running', 'waiting_approval')
  AND NOT EXISTS (SELECT 1 FROM agents WHERE agents.id = agent_children.id);

UPDATE agent_tasks
SET state = 'interrupted', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE state IN ('queued', 'running', 'waiting_approval')
  AND NOT EXISTS (SELECT 1 FROM agents WHERE agents.task_id = agent_tasks.id);
