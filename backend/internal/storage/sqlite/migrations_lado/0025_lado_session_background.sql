-- LADO fork: a session's live background tasks (background shells, Monitors
-- and subagents its agent started and that still run), as the Claude ACP
-- bridge reports them. One row a session; no row means AO does not know
-- (an older bridge, or no live provider). tasks_json is a JSON array of
-- {type, description, startedAt}; an empty array means none run. source is
-- 'bridge' for the bridge's own report and 'transcript' for what the LADO
-- watch read of a worker whose bridge cannot report (its conversation file
-- and process table); a bridge row always wins. The row goes with its
-- session. Keep this file ASCII: sqlc miscounts offsets after
-- non-ASCII text.

-- +goose Up
CREATE TABLE lado_session_background (
    session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    tasks_json TEXT NOT NULL CHECK (json_valid(tasks_json) AND json_type(tasks_json) = 'array'),
    source     TEXT NOT NULL DEFAULT 'bridge' CHECK (source IN ('bridge', 'transcript')),
    updated_at DATETIME NOT NULL
);

-- +goose Down
DROP TABLE lado_session_background;
