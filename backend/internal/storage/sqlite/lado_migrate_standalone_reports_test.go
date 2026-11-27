package sqlite

import (
	"context"
	"database/sql"
	"testing"
)

// reportProjectNotNull reads the declared nullability of reports.project_id.
func reportProjectNotNull(t *testing.T, db *sql.DB) int {
	t.Helper()
	var notNull int
	if err := db.QueryRow(`SELECT "notnull" FROM pragma_table_info('reports') WHERE name = 'project_id'`).Scan(&notNull); err != nil {
		t.Fatalf("read reports.project_id: %v", err)
	}
	return notNull
}

// 0027 lets a session without a project store reports (GitHub issue #8). Its
// down migration drops those reports and restores NOT NULL, keeping project
// reports; going up again widens the column once more.
func TestLadoMigrate0027StandaloneReports(t *testing.T) {
	ctx := context.Background()
	db := openLadoDatabase(t, 1)
	if got := reportProjectNotNull(t, db); got != 0 {
		t.Fatalf("reports.project_id notnull = %d after migrating, want 0", got)
	}

	mustExec(t, db, `INSERT INTO projects (id, path, registered_at) VALUES ('p', '/p', CURRENT_TIMESTAMP)`)
	mustExec(t, db, `INSERT INTO sessions (id, project_id, num, created_at, updated_at, activity_last_at)
		VALUES ('p-1', 'p', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
		       ('standalone-1', NULL, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	insertReport := `INSERT INTO reports (id, session_id, project_id, state, note, created_at, available_at)
		VALUES (?, ?, ?, 'checkpoint', 'note', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`
	mustExec(t, db, insertReport, "rpt_project", "p-1", "p")
	mustExec(t, db, insertReport, "rpt_standalone", "standalone-1", nil)
	mustExec(t, db, `INSERT INTO report_outputs (report_id, position, kind, reference)
		VALUES ('rpt_project', 0, 'artifact', 'kept'), ('rpt_standalone', 0, 'artifact', 'dropped')`)

	provider := ladoMigrationProvider(t, db)
	if _, err := provider.DownTo(ctx, 20); err != nil {
		t.Fatalf("down to 0020: %v", err)
	}
	if got := appliedVersion(t, db, ladoVersionTable); got != 20 {
		t.Fatalf("%s = %d, want 20", ladoVersionTable, got)
	}
	if got := reportProjectNotNull(t, db); got != 1 {
		t.Fatalf("reports.project_id notnull = %d after the down migration, want 1", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM reports WHERE id = 'rpt_project'`); got != 1 {
		t.Fatalf("project reports after the down migration = %d, want 1", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM reports WHERE project_id IS NULL`); got != 0 {
		t.Fatalf("reports without a project after the down migration = %d, want 0", got)
	}
	if got := countRows(t, db, `SELECT COUNT(*) FROM report_outputs`); got != 1 {
		t.Fatalf("report outputs after the down migration = %d, want only the project report's", got)
	}
	if _, err := db.Exec(insertReport, "rpt_refused", "standalone-1", nil); err == nil {
		t.Fatal("a report without a project was stored under NOT NULL")
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if got, want := appliedVersion(t, db, ladoVersionTable), latestLadoMigration(t); got != want {
		t.Fatalf("%s = %d after migrating up again, want %d", ladoVersionTable, got, want)
	}
	if got := reportProjectNotNull(t, db); got != 0 {
		t.Fatalf("reports.project_id notnull = %d after migrating up again, want 0", got)
	}
	mustExec(t, db, insertReport, "rpt_standalone_again", "standalone-1", nil)
}
