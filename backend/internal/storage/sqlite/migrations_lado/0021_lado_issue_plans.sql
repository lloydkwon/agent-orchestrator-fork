-- LADO fork: plans from the issue itself. The developer can skip the plan
-- worker and have LADO write the plan from the issue, pinned at once and
-- approved in the desktop app like any other plan. Such a request has kind
-- 'issue' and holds the session id 'lado:issue/<id>', which names no
-- session; its revision has source 'issue'.
--
-- SQLite cannot ALTER a CHECK constraint, so the two CHECKs are widened in
-- the stored schema, as 0202_allow_memcode_harness does on the upstream
-- track. writable_schema changes run outside a transaction; RESET forces
-- SQLite to reparse the schema. Existing rows keep their values.

-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, 'CHECK (kind IN (''orchestrator'', ''worker''))', 'CHECK (kind IN (''orchestrator'', ''worker'', ''issue''))')
WHERE type = 'table' AND name = 'lado_plan_requests'
  AND sql NOT LIKE '%''issue''%';
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, 'CHECK (source IN (''agent'', ''user''))', 'CHECK (source IN (''agent'', ''user'', ''issue''))')
WHERE type = 'table' AND name = 'lado_plan_revisions'
  AND sql NOT LIKE '%''issue''%';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, 'CHECK (kind IN (''orchestrator'', ''worker'', ''issue''))', 'CHECK (kind IN (''orchestrator'', ''worker''))')
WHERE type = 'table' AND name = 'lado_plan_requests';
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, 'CHECK (source IN (''agent'', ''user'', ''issue''))', 'CHECK (source IN (''agent'', ''user''))')
WHERE type = 'table' AND name = 'lado_plan_revisions';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
