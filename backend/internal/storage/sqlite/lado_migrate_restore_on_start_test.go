package sqlite

import (
	"context"
	"slices"
	"testing"
)

// 0024 adds restore_on_start, on for settings stored before it, takes 0 and 1
// only, and its down migration removes it.
func TestLadoMigrate0024AddsRestoreOnStartOn(t *testing.T) {
	ctx := context.Background()
	db := openLadoDatabase(t, 1)
	if got, want := appliedVersion(t, db, ladoVersionTable), latestLadoMigration(t); got != want || got < 24 {
		t.Fatalf("%s = %d, want %d", ladoVersionTable, got, want)
	}
	provider := ladoMigrationProvider(t, db)
	if _, err := provider.DownTo(ctx, 23); err != nil {
		t.Fatalf("down to 0023: %v", err)
	}
	if slices.Contains(tableColumns(t, db, "lado_dispatch_settings"), "restore_on_start") {
		t.Fatal("lado_dispatch_settings.restore_on_start survived the 0024 down migration")
	}
	mustExec(t, db, `INSERT INTO lado_dispatch_settings (id, max_concurrent, max_run_minutes, hold_on_memory_tight, updated_at)
		VALUES (1, 5, 1440, 1, CURRENT_TIMESTAMP)`)

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up from 0023: %v", err)
	}
	var on, tasks, minutes int
	if err := db.QueryRow(`SELECT restore_on_start, max_concurrent, max_run_minutes FROM lado_dispatch_settings WHERE id = 1`).Scan(&on, &tasks, &minutes); err != nil {
		t.Fatal(err)
	}
	if on != 1 || tasks != 5 || minutes != 1440 {
		t.Fatalf("stored row = restore_on_start %d, max_concurrent %d, max_run_minutes %d; want restore on and the caps kept", on, tasks, minutes)
	}
	if _, err := db.Exec(`UPDATE lado_dispatch_settings SET restore_on_start = 2 WHERE id = 1`); err == nil {
		t.Fatal("restore_on_start 2 was accepted")
	}
}
