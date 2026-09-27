-- Per-project auto-approve tier: '' (or 'normal'), 'auto_workspace', 'auto_all'.
ALTER TABLE projects ADD COLUMN approval_mode TEXT NOT NULL DEFAULT '';
