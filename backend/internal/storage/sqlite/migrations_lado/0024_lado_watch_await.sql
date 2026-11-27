-- LADO fork: how long a running worker may rest after its turn, without a
-- report, before LADO tells the owner it waits on them (a needs_input
-- notification). NULL follows the row below it; 0 tells only of a
-- needs-input report. Keep this file ASCII: sqlc miscounts offsets after
-- non-ASCII text.

-- +goose Up
ALTER TABLE lado_watch_settings ADD COLUMN await_minutes INTEGER;

-- +goose Down
ALTER TABLE lado_watch_settings DROP COLUMN await_minutes;
