package sqlite

import (
	"context"
	"slices"
	"testing"
)

// 0022 adds run_directly, on for a row stored before it, takes 0 and 1 only,
// and its down migration removes it.
func TestLadoMigrate0022AddsRunDirectlyOn(t *testing.T) {
	ctx := context.Background()
	db := openLadoDatabase(t, 1)
	if got, want := appliedVersion(t, db, ladoVersionTable), latestLadoMigration(t); got != want || got < 22 {
		t.Fatalf("%s = %d, want %d", ladoVersionTable, got, want)
	}
	provider := ladoMigrationProvider(t, db)
	if _, err := provider.DownTo(ctx, 21); err != nil {
		t.Fatalf("down to 0021: %v", err)
	}
	if slices.Contains(tableColumns(t, db, "lado_project_settings"), "run_directly") {
		t.Fatal("lado_project_settings.run_directly survived the 0022 down migration")
	}
	mustExec(t, db, `INSERT INTO lado_project_settings (project_id, lado_mode, updated_at) VALUES ('p', 1, CURRENT_TIMESTAMP)`)

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up from 0021: %v", err)
	}
	var on, mode int
	if err := db.QueryRow(`SELECT run_directly, lado_mode FROM lado_project_settings WHERE project_id = 'p'`).Scan(&on, &mode); err != nil {
		t.Fatal(err)
	}
	if on != 1 || mode != 1 {
		t.Fatalf("stored row = run_directly %d, lado_mode %d; want run directly on and LADO mode kept", on, mode)
	}
	if _, err := db.Exec(`UPDATE lado_project_settings SET run_directly = 2 WHERE project_id = 'p'`); err == nil {
		t.Fatal("run_directly 2 was accepted")
	}
}
