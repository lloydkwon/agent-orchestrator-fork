package sqlite

import (
	"encoding/json"
	"testing"
)

// LADO fork: an orchestrator notice publishes once, when LADO queues it, so
// the desktop app shows it too. The change names the notice only: its key and
// text stay on LADO's loopback routes, as the event stream also reaches the
// LAN. A repeated key, a notice of an unregistered project and a notice
// marked sent publish nothing.
func TestLadoOrchestratorNoticePublishesOnInsert(t *testing.T) {
	db := openLadoDatabase(t, 1)
	mustExec(t, db, `INSERT INTO projects (id, path, registered_at) VALUES ('p', '/p', CURRENT_TIMESTAMP)`)
	if !triggerExists(t, db, "lado_cdc_orchestrator_notices_insert") {
		t.Fatal("trigger lado_cdc_orchestrator_notices_insert is missing")
	}
	var seq int64
	if err := db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM change_log`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	insert := func(project, key string) {
		t.Helper()
		mustExec(t, db, `INSERT INTO lado_orchestrator_notices (project_id, notice_key, text, occurred_at, created_at)
			VALUES (?, ?, 'LADO 알림: #1 실행 시작', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
			ON CONFLICT (notice_key) DO NOTHING`, project, key)
	}
	insert("p", "started:p-1")
	insert("p", "started:p-1")
	insert("gone", "started:gone-1")
	mustExec(t, db, `UPDATE lado_orchestrator_notices SET sent_at = CURRENT_TIMESTAMP, sent_to = 'p-orchestrator-1'`)

	var id int64
	if err := db.QueryRow(`SELECT id FROM lado_orchestrator_notices WHERE notice_key = 'started:p-1'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT project_id, session_id IS NULL, event_type, payload FROM change_log WHERE seq > ? ORDER BY seq`, seq)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	type change struct {
		project, eventType string
		noSession          bool
		payload            map[string]any
	}
	var got []change
	for rows.Next() {
		var c change
		var raw string
		if err := rows.Scan(&c.project, &c.noSession, &c.eventType, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &c.payload); err != nil {
			t.Fatalf("payload %q: %v", raw, err)
		}
		got = append(got, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("changes = %+v, want one", got)
	}
	c := got[0]
	want := map[string]any{"kind": "lado_notice", "op": "insert", "noticeId": float64(id), "projectId": "p"}
	if c.project != "p" || !c.noSession || c.eventType != "session_updated" || len(c.payload) != len(want) {
		t.Fatalf("change = %+v, want %v in project p without a session", c, want)
	}
	for k, v := range want {
		if c.payload[k] != v {
			t.Fatalf("payload %s = %v, want %v (payload %v)", k, c.payload[k], v, c.payload)
		}
	}
}
