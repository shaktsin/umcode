-- Admission pins the canonical project root as well as the committed revision.
-- Existing tasks deliberately receive no root and cannot regain admission until
-- a new explicitly admitted task is created.
ALTER TABLE agent_tasks ADD COLUMN base_root TEXT NOT NULL DEFAULT '';
