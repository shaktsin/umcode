-- Task-wide actual totals and immutable initial limits survive follow-up turns.
CREATE TABLE agent_task_budgets (
 task_id TEXT PRIMARY KEY REFERENCES agent_tasks(id) ON DELETE CASCADE,
 limits_json TEXT NOT NULL,
 usage_json TEXT NOT NULL
);
