-- LADO fork: a project's default for running a new task without plan review.
-- When run_directly is 1, New Task's "Run without plan review" switch starts
-- on in the project: LADO writes the plan from the issue, and the developer
-- still approves it in the desktop app. Every project starts with it on,
-- existing rows included, and turns it off in its LADO settings.

-- +goose Up
ALTER TABLE lado_project_settings ADD COLUMN run_directly INTEGER NOT NULL DEFAULT 1 CHECK (run_directly IN (0, 1));

-- +goose Down
ALTER TABLE lado_project_settings DROP COLUMN run_directly;
