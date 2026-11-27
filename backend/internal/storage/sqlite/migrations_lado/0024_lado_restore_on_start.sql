-- LADO fork: sessions a reboot stopped start again when the daemon starts.
--
-- lado_dispatch_settings.restore_on_start says whether the daemon, at its
-- start, starts again the agent of each session that was running when it
-- last saw it and whose runtime is gone since (a reboot, or a crash of the
-- terminal server), in the session's own worktree and conversation. It is on
-- unless the developer turns it off in Settings → Runs; an existing row turns
-- it on. Off keeps upstream behavior: the agent stays stopped until a person
-- restores it.

-- +goose Up
ALTER TABLE lado_dispatch_settings ADD COLUMN restore_on_start INTEGER NOT NULL DEFAULT 1
    CHECK (restore_on_start IN (0, 1));

-- +goose Down
ALTER TABLE lado_dispatch_settings DROP COLUMN restore_on_start;
