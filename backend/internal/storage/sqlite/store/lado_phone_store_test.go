package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const ladoPhoneFingerprint = "3f9a1c0d2b4e5f60"

func ladoPhoneOnProof(nonce string, at time.Time) domain.LadoPresenceProof {
	return domain.LadoPresenceProof{
		Nonce: nonce, Action: domain.LadoPresencePhoneApprovalOn, SubjectID: "host.tail.ts.net",
		ExpiresAt: at.Add(time.Minute), UsedAt: at,
	}
}

func ladoPhoneEnrollProof(nonce string, at time.Time) domain.LadoPresenceProof {
	return domain.LadoPresenceProof{
		Nonce: nonce, Action: domain.LadoPresencePhonePasskeyEnroll, SubjectID: ladoPhoneFingerprint,
		ExpiresAt: at.Add(time.Minute), UsedAt: at,
	}
}

func ladoPhoneSettingsSample() domain.LadoPhoneSettings {
	return domain.LadoPhoneSettings{
		RPID: "host.tail.ts.net", Origin: "https://host.tail.ts.net:8444", OwnerLogin: "dev@example.com",
		UserHandle: []byte{0xa1, 0xb2, 0xc3}, ServePort: 8444, SocketPath: "/home/dev/.ao/lado/phone.sock",
	}
}

func ladoPhonePasskeySample(id string, credential []byte, fingerprint string, at time.Time) domain.LadoPhonePasskey {
	return domain.LadoPhonePasskey{
		ID: id, CredentialID: credential, CredentialJSON: `{"id":"` + id + `"}`, RPID: "host.tail.ts.net",
		DeviceHint: "iPhone", Login: "dev@example.com", Fingerprint: fingerprint, SignCount: 0, CreatedAt: at,
	}
}

func ladoPhoneEvent(action, subject string, at time.Time) domain.LadoAuditEvent {
	return domain.LadoAuditEvent{At: at, Action: action, SubjectKind: "daemon", SubjectID: subject, Payload: json.RawMessage(`{}`)}
}

// ladoPhoneAudits returns the actor of each audit event with action, oldest
// first.
func ladoPhoneAudits(t *testing.T, raw *sql.DB, action string) []string {
	t.Helper()
	rows, err := raw.Query(`SELECT actor FROM lado_audit_events WHERE action = ? ORDER BY id`, action)
	if err != nil {
		t.Fatalf("read audit events: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var actors []string
	for rows.Next() {
		var actor string
		if err := rows.Scan(&actor); err != nil {
			t.Fatalf("scan audit event: %v", err)
		}
		actors = append(actors, actor)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read audit events: %v", err)
	}
	return actors
}

func countLadoRows(t *testing.T, raw *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// The phone approval's settings, its passkeys and their audit events: a
// desktop proof turns it on and enrolls a passkey, each with its audit event
// in one transaction; a reused proof or credential leaves nothing behind;
// turning off and revoking are the API's.
func TestLadoPhonePasskeyStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, raw := openLadoAuditStore(t)
	at := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)

	off, err := s.LadoPhoneSettings(ctx)
	if err != nil || off.Enabled || off.RPID != "" || !off.UpdatedAt.IsZero() {
		t.Fatalf("settings before any = %+v, %v; want the zero value", off, err)
	}

	// Enrolling while phone approval is off is refused.
	err = s.InsertLadoPhonePasskey(ctx, ladoPhonePasskeySample("pk-early", []byte{9}, "early", at), ladoPhoneEnrollProof("enroll-early", at), ladoPhoneEvent("phone_passkey.enrolled", "pk-early", at))
	if !errors.Is(err, domain.ErrLadoPhoneApprovalOff) {
		t.Fatalf("enrolling while off = %v, want ErrLadoPhoneApprovalOff", err)
	}

	onProof := ladoPhoneOnProof("phone-on-0001", at)
	on, err := s.EnableLadoPhone(ctx, ladoPhoneSettingsSample(), onProof, ladoPhoneEvent("phone_approval.enabled", "phone_approval", at))
	if err != nil {
		t.Fatalf("EnableLadoPhone: %v", err)
	}
	want := ladoPhoneSettingsSample()
	want.Enabled, want.EnableProofNonce, want.UpdatedAt = true, onProof.Nonce, at
	if !ladoPhoneSettingsEqual(on, want) {
		t.Fatalf("enabled settings = %+v\nwant %+v", on, want)
	}
	if read, err := s.LadoPhoneSettings(ctx); err != nil || !ladoPhoneSettingsEqual(read, want) {
		t.Fatalf("settings read back = %+v, %v\nwant %+v", read, err, want)
	}
	if actors := ladoPhoneAudits(t, raw, "phone_approval.enabled"); len(actors) != 1 || actors[0] != "human" {
		t.Fatalf("enable audits = %v, want one by human", actors)
	}

	enrollProof := ladoPhoneEnrollProof("enroll-0001", at)
	passkey := ladoPhonePasskeySample("pk-1", []byte{0x01, 0x02, 0x03}, ladoPhoneFingerprint, at)
	passkey.SignCount = 4
	if err := s.InsertLadoPhonePasskey(ctx, passkey, enrollProof, ladoPhoneEvent("phone_passkey.enrolled", "pk-1", at)); err != nil {
		t.Fatalf("InsertLadoPhonePasskey: %v", err)
	}
	passkey.EnrollProofNonce = enrollProof.Nonce
	got, ok, err := s.LadoPhonePasskeyByCredentialID(ctx, []byte{0x01, 0x02, 0x03})
	if err != nil || !ok || !ladoPhonePasskeyEqual(got, passkey) {
		t.Fatalf("passkey by credential = %+v, %v, %v\nwant %+v", got, ok, err, passkey)
	}
	if _, ok, err := s.LadoPhonePasskeyByCredentialID(ctx, []byte{0x01, 0x02}); err != nil || ok {
		t.Fatalf("an unknown credential = %v, %v; want not found", ok, err)
	}
	if actors := ladoPhoneAudits(t, raw, "phone_passkey.enrolled"); len(actors) != 1 || actors[0] != "human" {
		t.Fatalf("enroll audits = %v, want one by human", actors)
	}

	t.Run("a refused enrollment leaves nothing", func(t *testing.T) {
		proofs, passkeys, audits := countLadoRows(t, raw, "lado_presence_proofs"), countLadoRows(t, raw, "lado_phone_passkeys"), countLadoRows(t, raw, "lado_audit_events")
		cases := []struct {
			name    string
			passkey domain.LadoPhonePasskey
			proof   domain.LadoPresenceProof
			want    error
		}{
			{"the same credential", ladoPhonePasskeySample("pk-2", []byte{0x01, 0x02, 0x03}, "other-fp", at), ladoPhoneEnrollProof("enroll-0002", at), domain.ErrLadoPhonePasskeyExists},
			{"the same proof", ladoPhonePasskeySample("pk-3", []byte{0x04}, "fp-3", at), enrollProof, domain.ErrLadoPresenceReplayed},
			{"a development proof", ladoPhonePasskeySample("pk-4", []byte{0x05}, "fp-4", at), func() domain.LadoPresenceProof {
				p := ladoPhoneEnrollProof("enroll-dev", at)
				p.Dev = true
				return p
			}(), domain.ErrLadoPhoneProofRefused},
			{"a passkey's proof", ladoPhonePasskeySample("pk-5", []byte{0x06}, "fp-5", at), func() domain.LadoPresenceProof {
				p := ladoPhoneEnrollProof("enroll-pk", at)
				p.Via = domain.LadoPresenceViaPasskey + ladoPhoneFingerprint
				return p
			}(), domain.ErrLadoPhoneProofRefused},
			{"a proof for another action", ladoPhonePasskeySample("pk-6", []byte{0x07}, "fp-6", at), ladoPhoneOnProof("enroll-other", at), domain.ErrLadoPhoneProofRefused},
			{"another relying party", func() domain.LadoPhonePasskey {
				p := ladoPhonePasskeySample("pk-7", []byte{0x08}, "fp-7", at)
				p.RPID = "other.tail.ts.net"
				return p
			}(), ladoPhoneEnrollProof("enroll-rp", at), domain.ErrLadoPhoneApprovalOff},
		}
		for _, tc := range cases {
			err := s.InsertLadoPhonePasskey(ctx, tc.passkey, tc.proof, ladoPhoneEvent("phone_passkey.enrolled", tc.passkey.ID, at))
			if !errors.Is(err, tc.want) {
				t.Errorf("%s = %v, want %v", tc.name, err, tc.want)
			}
		}
		if p, k, a := countLadoRows(t, raw, "lado_presence_proofs"), countLadoRows(t, raw, "lado_phone_passkeys"), countLadoRows(t, raw, "lado_audit_events"); p != proofs || k != passkeys || a != audits {
			t.Errorf("rows after refusals = %d proofs, %d passkeys, %d audits; want %d, %d, %d", p, k, a, proofs, passkeys, audits)
		}
	})

	t.Run("an actor other than the proof's", func(t *testing.T) {
		event := ladoPhoneEvent("phone_passkey.enrolled", "pk-8", at)
		event.Actor = "api"
		err := s.InsertLadoPhonePasskey(ctx, ladoPhonePasskeySample("pk-8", []byte{0x09}, "fp-8", at), ladoPhoneEnrollProof("enroll-actor", at), event)
		if err == nil {
			t.Fatal("an enrollment audited as api was accepted")
		}
	})

	// A use moves the sign count; a clone warning stays once seen.
	used := at.Add(time.Hour)
	if err := s.RecordLadoPhonePasskeyUse(ctx, "pk-1", 7, true, used); err != nil {
		t.Fatalf("RecordLadoPhonePasskeyUse: %v", err)
	}
	if err := s.RecordLadoPhonePasskeyUse(ctx, "pk-1", 8, false, used.Add(time.Minute)); err != nil {
		t.Fatalf("RecordLadoPhonePasskeyUse again: %v", err)
	}
	got, _, _ = s.LadoPhonePasskeyByCredentialID(ctx, []byte{0x01, 0x02, 0x03})
	if got.SignCount != 8 || !got.CloneWarning || !got.LastUsedAt.Equal(used.Add(time.Minute)) {
		t.Fatalf("after two uses = sign count %d, clone warning %v, last used %v; want 8, true, %v", got.SignCount, got.CloneWarning, got.LastUsedAt, used.Add(time.Minute))
	}
	if err := s.RecordLadoPhonePasskeyUse(ctx, "pk-missing", 1, false, used); !errors.Is(err, domain.ErrLadoPhonePasskeyNotFound) {
		t.Fatalf("a use of an unknown passkey = %v, want ErrLadoPhonePasskeyNotFound", err)
	}

	// Revoking keeps the row, once; a revoked passkey takes no use.
	revoked := used.Add(2 * time.Hour)
	now, err := s.RevokeLadoPhonePasskey(ctx, "pk-1", ladoPhoneEvent("phone_passkey.revoked", "pk-1", revoked))
	if err != nil || !now {
		t.Fatalf("RevokeLadoPhonePasskey = %v, %v; want revoked now", now, err)
	}
	if again, err := s.RevokeLadoPhonePasskey(ctx, "pk-1", ladoPhoneEvent("phone_passkey.revoked", "pk-1", revoked.Add(time.Minute))); err != nil || again {
		t.Fatalf("revoking again = %v, %v; want nothing to do", again, err)
	}
	if _, err := s.RevokeLadoPhonePasskey(ctx, "pk-missing", ladoPhoneEvent("phone_passkey.revoked", "pk-missing", revoked)); !errors.Is(err, domain.ErrLadoPhonePasskeyNotFound) {
		t.Fatalf("revoking an unknown passkey = %v, want ErrLadoPhonePasskeyNotFound", err)
	}
	if actors := ladoPhoneAudits(t, raw, "phone_passkey.revoked"); len(actors) != 1 || actors[0] != "api" {
		t.Fatalf("revoke audits = %v, want one by api", actors)
	}
	if err := s.RecordLadoPhonePasskeyUse(ctx, "pk-1", 9, false, revoked); !errors.Is(err, domain.ErrLadoPhonePasskeyNotFound) {
		t.Fatalf("a use of a revoked passkey = %v, want ErrLadoPhonePasskeyNotFound", err)
	}
	all, err := s.LadoPhonePasskeys(ctx)
	if err != nil || len(all) != 1 || all[0].ID != "pk-1" || !all[0].Revoked() || !all[0].RevokedAt.Equal(revoked) || all[0].SignCount != 8 {
		t.Fatalf("passkeys = %+v, %v; want pk-1 revoked at %v", all, err, revoked)
	}
	// A revoked credential cannot be enrolled again.
	err = s.InsertLadoPhonePasskey(ctx, ladoPhonePasskeySample("pk-9", []byte{0x01, 0x02, 0x03}, "fp-9", revoked), ladoPhoneEnrollProof("enroll-again", revoked), ladoPhoneEvent("phone_passkey.enrolled", "pk-9", revoked))
	if !errors.Is(err, domain.ErrLadoPhonePasskeyExists) {
		t.Fatalf("enrolling a revoked credential again = %v, want ErrLadoPhonePasskeyExists", err)
	}

	// Turning off is the API's and keeps what turning on fixed; turning off
	// what is off records nothing.
	offAt := revoked.Add(time.Hour)
	off, changed, err := s.DisableLadoPhone(ctx, ladoPhoneEvent("phone_approval.disabled", "phone_approval", offAt))
	if err != nil || !changed || off.Enabled || off.RPID != want.RPID || !bytes.Equal(off.UserHandle, want.UserHandle) || !off.UpdatedAt.Equal(offAt) {
		t.Fatalf("DisableLadoPhone = %+v, %v, %v; want off with its relying party kept", off, changed, err)
	}
	if _, changed, err := s.DisableLadoPhone(ctx, ladoPhoneEvent("phone_approval.disabled", "phone_approval", offAt.Add(time.Minute))); err != nil || changed {
		t.Fatalf("turning off again = %v, %v; want nothing to do", changed, err)
	}
	if actors := ladoPhoneAudits(t, raw, "phone_approval.disabled"); len(actors) != 1 || actors[0] != "api" {
		t.Fatalf("disable audits = %v, want one by api", actors)
	}

	// Turning on needs a desktop proof of its action, used once.
	for name, proof := range map[string]domain.LadoPresenceProof{
		"a development proof": func() domain.LadoPresenceProof {
			p := ladoPhoneOnProof("phone-on-dev", offAt)
			p.Dev = true
			return p
		}(),
		"a passkey's proof": func() domain.LadoPresenceProof {
			p := ladoPhoneOnProof("phone-on-pk", offAt)
			p.Via = domain.LadoPresenceViaPasskey + ladoPhoneFingerprint
			return p
		}(),
		"a proof for another action": ladoPhoneEnrollProof("phone-on-other", offAt),
	} {
		if _, err := s.EnableLadoPhone(ctx, ladoPhoneSettingsSample(), proof, ladoPhoneEvent("phone_approval.enabled", "phone_approval", offAt)); !errors.Is(err, domain.ErrLadoPhoneProofRefused) {
			t.Errorf("turning on with %s = %v, want ErrLadoPhoneProofRefused", name, err)
		}
	}
	if _, err := s.EnableLadoPhone(ctx, ladoPhoneSettingsSample(), onProof, ladoPhoneEvent("phone_approval.enabled", "phone_approval", offAt)); !errors.Is(err, domain.ErrLadoPresenceReplayed) {
		t.Fatalf("turning on with a used proof = %v, want ErrLadoPresenceReplayed", err)
	}
	if read, _ := s.LadoPhoneSettings(ctx); read.Enabled || read.EnableProofNonce != onProof.Nonce {
		t.Fatalf("settings after refusals = %+v, want still off", read)
	}
	if actors := ladoPhoneAudits(t, raw, "phone_approval.enabled"); len(actors) != 1 {
		t.Fatalf("enable audits after refusals = %v, want the first only", actors)
	}
	if broken, err := s.VerifyLadoAuditChain(ctx); err != nil || broken != 0 {
		t.Fatalf("audit chain = (%d, %v)", broken, err)
	}
}

func ladoPhoneSettingsEqual(a, b domain.LadoPhoneSettings) bool {
	return a.Enabled == b.Enabled && a.RPID == b.RPID && a.Origin == b.Origin && a.OwnerLogin == b.OwnerLogin &&
		bytes.Equal(a.UserHandle, b.UserHandle) && a.ServePort == b.ServePort && a.SocketPath == b.SocketPath &&
		a.EnableProofNonce == b.EnableProofNonce && a.UpdatedAt.Equal(b.UpdatedAt)
}

func ladoPhonePasskeyEqual(a, b domain.LadoPhonePasskey) bool {
	return a.ID == b.ID && bytes.Equal(a.CredentialID, b.CredentialID) && a.CredentialJSON == b.CredentialJSON &&
		a.RPID == b.RPID && a.DeviceHint == b.DeviceHint && a.Login == b.Login && a.Fingerprint == b.Fingerprint &&
		a.SignCount == b.SignCount && a.CloneWarning == b.CloneWarning && a.CreatedAt.Equal(b.CreatedAt) &&
		a.LastUsedAt.Equal(b.LastUsedAt) && a.RevokedAt.Equal(b.RevokedAt) && a.EnrollProofNonce == b.EnrollProofNonce
}
