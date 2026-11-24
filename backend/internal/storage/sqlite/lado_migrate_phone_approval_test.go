package sqlite

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
)

// 0023 adds the proofs' via, ” for a proof stored before it, the phone
// approval's one settings row and the enrolled passkeys, with the rules the
// store relies on, and its down migration removes them.
func TestMigrateLadoPhoneApproval(t *testing.T) {
	ctx := context.Background()
	db := openLadoDatabase(t, 1)
	if got, want := appliedVersion(t, db, ladoVersionTable), latestLadoMigration(t); got != want || got < 23 {
		t.Fatalf("%s = %d, want %d (at least 23)", ladoVersionTable, got, want)
	}
	provider := ladoMigrationProvider(t, db)
	if _, err := provider.DownTo(ctx, 22); err != nil {
		t.Fatalf("down to 0022: %v", err)
	}
	if slices.Contains(tableColumns(t, db, "lado_presence_proofs"), "via") {
		t.Fatal("lado_presence_proofs.via survived the 0023 down migration")
	}
	for _, table := range []string{"lado_phone_settings", "lado_phone_passkeys"} {
		if len(tableColumns(t, db, table)) != 0 {
			t.Fatalf("%s survived the 0023 down migration", table)
		}
	}
	mustExec(t, db, `INSERT INTO lado_presence_proofs (nonce, action, subject_id, expires_at, used_at)
VALUES ('before-0023', 'plan.approve', 'rev-1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up from 0022: %v", err)
	}
	var via string
	if err := db.QueryRow(`SELECT via FROM lado_presence_proofs WHERE nonce = 'before-0023'`).Scan(&via); err != nil || via != "" {
		t.Fatalf("via of a proof stored before 0023 = %q, %v; want ''", via, err)
	}
	for _, want := range [][2]string{
		{"lado_phone_settings", "enabled"}, {"lado_phone_settings", "rp_id"}, {"lado_phone_settings", "origin"},
		{"lado_phone_settings", "owner_login"}, {"lado_phone_settings", "user_handle"}, {"lado_phone_settings", "serve_port"},
		{"lado_phone_settings", "socket_path"}, {"lado_phone_settings", "enable_proof_nonce"}, {"lado_phone_settings", "updated_at"},
		{"lado_phone_passkeys", "credential_id"}, {"lado_phone_passkeys", "credential_json"}, {"lado_phone_passkeys", "rp_id"},
		{"lado_phone_passkeys", "device_hint"}, {"lado_phone_passkeys", "login"}, {"lado_phone_passkeys", "fingerprint"},
		{"lado_phone_passkeys", "sign_count"}, {"lado_phone_passkeys", "clone_warning"}, {"lado_phone_passkeys", "created_at"},
		{"lado_phone_passkeys", "last_used_at"}, {"lado_phone_passkeys", "revoked_at"}, {"lado_phone_passkeys", "enroll_proof_nonce"},
	} {
		if !slices.Contains(tableColumns(t, db, want[0]), want[1]) {
			t.Errorf("%s has no column %s", want[0], want[1])
		}
	}

	mustExec(t, db, `INSERT INTO lado_presence_proofs (nonce, action, subject_id, expires_at, used_at)
VALUES ('on-1', 'phone_approval.on', 'host.tail.ts.net', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
       ('enroll-1', 'phone_passkey.enroll', 'pairing-1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	refused := map[string]string{
		"a second settings row": `INSERT INTO lado_phone_settings (id, updated_at) VALUES (2, CURRENT_TIMESTAMP)`,
		"on without a proof": `INSERT INTO lado_phone_settings (id, enabled, rp_id, origin, owner_login, updated_at)
VALUES (1, 1, 'host.tail.ts.net', 'https://host.tail.ts.net:8444', 'dev@example.com', CURRENT_TIMESTAMP)`,
		"on without a relying party": `INSERT INTO lado_phone_settings (id, enabled, origin, owner_login, enable_proof_nonce, updated_at)
VALUES (1, 1, 'https://host.tail.ts.net:8444', 'dev@example.com', 'on-1', CURRENT_TIMESTAMP)`,
		"on with an unknown proof": `INSERT INTO lado_phone_settings (id, enabled, rp_id, origin, owner_login, enable_proof_nonce, updated_at)
VALUES (1, 1, 'host.tail.ts.net', 'https://host.tail.ts.net:8444', 'dev@example.com', 'missing', CURRENT_TIMESTAMP)`,
		"a passkey proof of development": `INSERT INTO lado_presence_proofs (nonce, action, subject_id, dev, via, expires_at, used_at)
VALUES ('dev-pk', 'plan.approve', 'rev-1', 1, 'passkey:abcd', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		"a via that names no passkey": `INSERT INTO lado_presence_proofs (nonce, action, subject_id, via, expires_at, used_at)
VALUES ('odd-via', 'plan.approve', 'rev-1', 'phone', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		"a credential id as text": `INSERT INTO lado_phone_passkeys (id, credential_id, credential_json, rp_id, login, fingerprint, created_at, enroll_proof_nonce)
VALUES ('pk-text', 'abc', '{}', 'host.tail.ts.net', 'dev@example.com', 'fp-text', CURRENT_TIMESTAMP, 'enroll-1')`,
		"a passkey without its proof": `INSERT INTO lado_phone_passkeys (id, credential_id, credential_json, rp_id, login, fingerprint, created_at, enroll_proof_nonce)
VALUES ('pk-orphan', X'0102', '{}', 'host.tail.ts.net', 'dev@example.com', 'fp-orphan', CURRENT_TIMESTAMP, 'missing')`,
	}
	for name, stmt := range refused {
		requireLadoConstraint(t, db, name, stmt)
	}
	mustExec(t, db, `INSERT INTO lado_phone_settings (id, enabled, rp_id, origin, owner_login, enable_proof_nonce, updated_at)
VALUES (1, 1, 'host.tail.ts.net', 'https://host.tail.ts.net:8444', 'dev@example.com', 'on-1', CURRENT_TIMESTAMP)`)
	mustExec(t, db, `INSERT INTO lado_presence_proofs (nonce, action, subject_id, via, expires_at, used_at)
VALUES ('pk-approve', 'plan.approve', 'rev-1', 'passkey:abcd', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	mustExec(t, db, `INSERT INTO lado_phone_passkeys (id, credential_id, credential_json, rp_id, login, fingerprint, created_at, enroll_proof_nonce)
VALUES ('pk-1', X'0102', '{}', 'host.tail.ts.net', 'dev@example.com', 'abcd', CURRENT_TIMESTAMP, 'enroll-1')`)
	mustExec(t, db, `INSERT INTO lado_presence_proofs (nonce, action, subject_id, expires_at, used_at)
VALUES ('enroll-2', 'phone_passkey.enroll', 'pairing-2', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	for name, stmt := range map[string]string{
		"the same credential twice": `INSERT INTO lado_phone_passkeys (id, credential_id, credential_json, rp_id, login, fingerprint, created_at, enroll_proof_nonce)
VALUES ('pk-2', X'0102', '{}', 'host.tail.ts.net', 'dev@example.com', 'efgh', CURRENT_TIMESTAMP, 'enroll-2')`,
		"one enrollment proof for two passkeys": `INSERT INTO lado_phone_passkeys (id, credential_id, credential_json, rp_id, login, fingerprint, created_at, enroll_proof_nonce)
VALUES ('pk-3', X'0304', '{}', 'host.tail.ts.net', 'dev@example.com', 'ijkl', CURRENT_TIMESTAMP, 'enroll-1')`,
		"the same fingerprint twice": `INSERT INTO lado_phone_passkeys (id, credential_id, credential_json, rp_id, login, fingerprint, created_at, enroll_proof_nonce)
VALUES ('pk-4', X'0506', '{}', 'host.tail.ts.net', 'dev@example.com', 'abcd', CURRENT_TIMESTAMP, 'enroll-2')`,
	} {
		requireLadoConstraint(t, db, name, stmt)
	}
}

// requireLadoConstraint fails unless a constraint refuses stmt, so a typo
// in stmt does not pass for a refusal.
func requireLadoConstraint(t *testing.T, db *sql.DB, name, stmt string) {
	t.Helper()
	_, err := db.Exec(stmt)
	if err == nil {
		t.Errorf("%s was accepted", name)
		return
	}
	if !strings.Contains(err.Error(), "constraint failed") {
		t.Errorf("%s = %v, want a constraint to refuse it", name, err)
	}
}
