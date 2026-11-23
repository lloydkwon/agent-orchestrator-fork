package sqlite

import (
	"context"
	"database/sql"
	"testing"
)

// 0021 lets a plan request have kind 'issue' and a revision source 'issue',
// keeps what is stored, still refuses other values, and its down migration
// narrows both CHECKs again.
func TestLadoMigrate0021AllowsPlansFromTheIssue(t *testing.T) {
	ctx := context.Background()
	db := openLadoDatabase(t, 1)
	if got, want := appliedVersion(t, db, ladoVersionTable), latestLadoMigration(t); got != want || got < 21 {
		t.Fatalf("%s = %d, want %d", ladoVersionTable, got, want)
	}
	provider := ladoMigrationProvider(t, db)
	if _, err := provider.DownTo(ctx, 20); err != nil {
		t.Fatalf("down to 0020: %v", err)
	}
	insertLadoSnapshotFixture(t, db)
	wantIssueValues(t, db, false)

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up from 0020: %v", err)
	}
	var kind, source string
	if err := db.QueryRow(`SELECT r.kind, v.source FROM lado_plan_requests r JOIN lado_plan_revisions v ON v.request_id = r.id WHERE r.id = 'request'`).
		Scan(&kind, &source); err != nil {
		t.Fatalf("read the stored request: %v", err)
	}
	if kind != "orchestrator" || source != "agent" {
		t.Fatalf("stored request = kind %q, source %q; want them kept", kind, source)
	}
	wantIssueValues(t, db, true)
	for _, query := range []string{
		`UPDATE lado_plan_requests SET kind = 'other' WHERE id = 'request'`,
		`UPDATE lado_plan_revisions SET source = 'other' WHERE id = 'revision'`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatalf("%s was accepted", query)
		}
	}

	if _, err := provider.DownTo(ctx, 20); err != nil {
		t.Fatalf("down to 0020 again: %v", err)
	}
	wantIssueValues(t, db, false)
}

// wantIssueValues checks whether the stored request and revision take
// 'issue', and puts their values back.
func wantIssueValues(t *testing.T, db *sql.DB, allowed bool) {
	t.Helper()
	for _, c := range []struct{ set, reset string }{
		{`UPDATE lado_plan_requests SET kind = 'issue' WHERE id = 'request'`, `UPDATE lado_plan_requests SET kind = 'orchestrator' WHERE id = 'request'`},
		{`UPDATE lado_plan_revisions SET source = 'issue' WHERE id = 'revision'`, `UPDATE lado_plan_revisions SET source = 'agent' WHERE id = 'revision'`},
	} {
		_, err := db.Exec(c.set)
		switch {
		case allowed && err != nil:
			t.Fatalf("%s: %v", c.set, err)
		case !allowed && err == nil:
			t.Fatalf("%s was accepted", c.set)
		}
		mustExec(t, db, c.reset)
	}
}
