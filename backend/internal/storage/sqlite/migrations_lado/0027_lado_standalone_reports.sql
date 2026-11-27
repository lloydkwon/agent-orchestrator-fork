-- +goose NO TRANSACTION
-- LADO fork: a session without a project (ao spawn --standalone) may store
-- ao report records (GitHub issue #8). Upstream 0153 declared
-- reports.project_id NOT NULL, so such a report could not be stored.
--
-- This is the one LADO migration that changes an upstream table (an
-- exception to docs/LJY-PRD/adr/0004-data-same-db-separate-migration-track.md,
-- approved for issue #8). It follows upstream 0140_standalone_sessions.sql:
-- SQLite has no ALTER COLUMN, and rebuilding reports would drop the
-- report_outputs_pr_created_cdc trigger's target and the delivery indexes, so
-- only the project_id declaration in sqlite_schema is widened. The on-disk
-- record format does not change. The replacement is exact and guarded by a
-- postcondition, so a schema that does not match fails the migration.
--
-- A report with a NULL project_id has no orchestrator to deliver it to:
-- the delivery schedule queries skip it (queries/reports.sql), and it stays
-- pending and visible in its session's report list.

-- +goose Up
-- +goose StatementBegin
PRAGMA writable_schema=ON;

UPDATE sqlite_schema
SET sql = replace(
    sql,
    'project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE',
    'project_id TEXT REFERENCES projects(id) ON DELETE CASCADE'
)
WHERE type = 'table' AND name = 'reports';

-- RESET turns writable_schema off and makes this connection reload the
-- schema before goose records the migration and the store starts.
PRAGMA writable_schema=RESET;

CREATE TEMP TABLE lado_standalone_reports_guard (
    nullable_project_columns INTEGER CHECK (nullable_project_columns = 1)
);
INSERT INTO lado_standalone_reports_guard
SELECT COUNT(*) FROM pragma_table_info('reports') WHERE name = 'project_id' AND "notnull" = 0;
DROP TABLE lado_standalone_reports_guard;

PRAGMA foreign_key_check;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Reports of sessions without a project cannot be kept under NOT NULL.
DELETE FROM report_outputs
WHERE report_id IN (SELECT id FROM reports WHERE project_id IS NULL);
DELETE FROM reports WHERE project_id IS NULL;

PRAGMA writable_schema=ON;

UPDATE sqlite_schema
SET sql = replace(
    sql,
    'project_id TEXT REFERENCES projects(id) ON DELETE CASCADE',
    'project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE'
)
WHERE type = 'table' AND name = 'reports';

PRAGMA writable_schema=RESET;

CREATE TEMP TABLE lado_standalone_reports_guard (
    required_project_columns INTEGER CHECK (required_project_columns = 1)
);
INSERT INTO lado_standalone_reports_guard
SELECT COUNT(*) FROM pragma_table_info('reports') WHERE name = 'project_id' AND "notnull" = 1;
DROP TABLE lado_standalone_reports_guard;

PRAGMA foreign_key_check;
-- +goose StatementEnd
