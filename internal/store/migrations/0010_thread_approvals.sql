-- Approval autonomy belongs to one chat, not every chat in its project.
ALTER TABLE threads ADD COLUMN approval_mode TEXT NOT NULL DEFAULT '';

-- Preserve the old project-level choice for existing conversations once, then
-- let each chat evolve independently.
UPDATE threads
SET approval_mode = COALESCE((
    SELECT approval_mode FROM projects WHERE projects.id = threads.project_id
), '')
WHERE project_id != '';

-- "Always allow here" is scoped to this chat, never the whole project.
CREATE TABLE IF NOT EXISTS thread_approvals (
    thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
    tool       TEXT NOT NULL,
    signature  TEXT NOT NULL,
    decision   TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (thread_id, tool, signature)
);
