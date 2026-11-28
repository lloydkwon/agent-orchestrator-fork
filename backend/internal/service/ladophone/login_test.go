package ladophone

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
)

// otherSigner signs as the enrolled credential with another private key, so
// its assertions name an enrolled passkey and do not verify.
func otherSigner(t *testing.T, k *softKey) *softKey {
	t.Helper()
	o := newSoftKey(t)
	o.id, o.user, o.count = k.id, k.user, k.count
	return o
}

func TestLoginGivesAReadOnlySession(t *testing.T) {
	h := newApprovalHarness(t)
	wantError(t, "list without a session", h.send(http.MethodGet, "/api/approvals", nil, ""), http.StatusUnauthorized, "LADO_PHONE_LOGIN_REQUIRED")

	b := h.loginBegin()
	pk := b.Options.PublicKey
	if b.Result != "ok" || b.Ceremony == "" || pk.RPID != testDNS || pk.UserVerification != "required" ||
		len(pk.AllowCredentials) != 1 || pk.AllowCredentials[0].ID != b64(h.key.id) {
		t.Fatalf("login begin = %+v", b)
	}
	h.clock.advance(time.Second)
	result, cookie := h.loginFinish(b.Ceremony, h.key.get(t, b.asBegin(), iPhoneFlags))
	if result != "ok" || cookie == nil {
		t.Fatalf("login finish = %q, cookie %v\n%s", result, cookie, h.logs)
	}
	if cookie.Name != "__Host-lado-phone" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode ||
		cookie.Path != "/" || cookie.Domain != "" || len(cookie.Value) < 43 {
		t.Fatalf("session cookie = %+v", cookie)
	}
	if stored := h.passkeys()[0]; !stored.LastUsedAt.Equal(h.clock.Now()) {
		t.Fatalf("last use = %v, want %v", stored.LastUsedAt, h.clock.Now())
	}
	w := h.send(http.MethodGet, "/api/approvals", nil, cookie.Value)
	if w.Code != http.StatusOK {
		t.Fatalf("list with a session: %d %s", w.Code, w.Body)
	}
	if list := decodeInto[listView](t, w); len(list.Items) != 2 {
		t.Fatalf("approvals = %+v, want the plan and the integration", list.Items)
	}
	// A cookie that is not a session's reads nothing.
	wantError(t, "made-up cookie", h.send(http.MethodGet, "/api/approvals", nil, randomToken()), http.StatusUnauthorized, "LADO_PHONE_LOGIN_REQUIRED")
	// The ceremony logs in once.
	if again, c := h.loginFinish(b.Ceremony, h.key.get(t, b.asBegin(), iPhoneFlags)); again != "stale" || c != nil {
		t.Fatalf("the same login again = %q, %v", again, c)
	}
	// A login without user verification fails.
	b = h.loginBegin()
	if res, c := h.loginFinish(b.Ceremony, h.key.get(t, b.asBegin(), protocol.FlagUserPresent|protocol.FlagBackupEligible|protocol.FlagBackupState)); res != "failed" || c != nil {
		t.Fatalf("login without user verification = %q", res)
	}

	// Revoking the passkey ends its sessions, and nothing is left to log in
	// with.
	if _, err := h.s.RevokePasskey(context.Background(), h.passkey.ID); err != nil {
		t.Fatal(err)
	}
	wantError(t, "list after revoking", h.send(http.MethodGet, "/api/approvals", nil, cookie.Value), http.StatusUnauthorized, "LADO_PHONE_LOGIN_REQUIRED")
	if b := h.loginBegin(); b.Result != "none" {
		t.Fatalf("login begin without a passkey = %q, want none", b.Result)
	}
}

func TestLoginLockoutAfterTenFailures(t *testing.T) {
	h := newApprovalHarness(t)
	stranger := newSoftKey(t)
	fail := func(i int, signer *softKey) {
		t.Helper()
		b := h.loginBegin()
		if b.Result != "ok" {
			t.Fatalf("failure %d: begin = %q", i, b.Result)
		}
		if res, c := h.loginFinish(b.Ceremony, signer.get(t, b.asBegin(), iPhoneFlags)); res != "failed" || c != nil {
			t.Fatalf("failure %d: finish = %q", i, res)
		}
	}
	// An enrolled passkey's signature that does not verify is audited and
	// raised on the desktop at once.
	fail(0, otherSigner(t, h.key))
	if got := h.alerts.kinds(); !slices.Equal(got, []string{AlertSignatureFailed}) {
		t.Fatalf("alerts after a bad signature = %v", got)
	}
	if ev := h.audits.last(t, "phone_passkey.signature_failed"); ev.Actor != "daemon" || ev.SubjectID != h.passkey.ID {
		t.Fatalf("signature audit = %+v", ev)
	}
	// An unknown credential fails without an alert.
	for i := 1; i < 9; i++ {
		fail(i, stranger)
	}
	if got := h.alerts.kinds(); len(got) != 1 {
		t.Fatalf("alerts = %v", got)
	}
	// The window is ten minutes: nine old failures and one new do not lock.
	h.clock.advance(loginFailureWindow + time.Second)
	fail(9, stranger)
	if b := h.loginBegin(); b.Result != "ok" {
		t.Fatalf("begin after failures spread over the window = %q", b.Result)
	}
	for i := 10; i < 19; i++ {
		fail(i, stranger)
	}
	// Ten failures within ten minutes: logins rest five minutes, even with
	// the right passkey, and the desktop hears of it once.
	if b := h.loginBegin(); b.Result != "locked" {
		t.Fatalf("begin after ten failures = %q, want locked", b.Result)
	}
	if got := h.alerts.kinds(); !slices.Equal(got, []string{AlertSignatureFailed, AlertLoginLocked}) {
		t.Fatalf("alerts after the lockout = %v", got)
	}
	h.audits.last(t, "phone_approval.login_locked")
	h.clock.advance(loginLockout - time.Second)
	if b := h.loginBegin(); b.Result != "locked" {
		t.Fatalf("begin just before the lockout ends = %q", b.Result)
	}
	h.clock.advance(2 * time.Second)
	h.login()
}

func TestLoginFinishRefusedWhileLocked(t *testing.T) {
	h := newApprovalHarness(t)
	early := h.loginBegin()
	stranger := newSoftKey(t)
	for i := 0; i < loginMaxFailures; i++ {
		b := h.loginBegin()
		h.loginFinish(b.Ceremony, stranger.get(t, b.asBegin(), iPhoneFlags))
	}
	if res, c := h.loginFinish(early.Ceremony, h.key.get(t, early.asBegin(), iPhoneFlags)); res != "locked" || c != nil {
		t.Fatalf("a login begun before the lockout = %q, want locked", res)
	}
}

func TestSessionIdleExpiry(t *testing.T) {
	h := newApprovalHarness(t)
	cookie := h.login()
	for i := 0; i < 3; i++ {
		h.clock.advance(sessionIdle - time.Minute)
		if w := h.send(http.MethodGet, "/api/approvals", nil, cookie); w.Code != http.StatusOK {
			t.Fatalf("read %d after %v idle: %d", i, sessionIdle-time.Minute, w.Code)
		}
	}
	h.clock.advance(sessionIdle + time.Second)
	wantError(t, "read after the idle time", h.send(http.MethodGet, "/api/approvals", nil, cookie), http.StatusUnauthorized, "LADO_PHONE_LOGIN_REQUIRED")

	// At most maxSessions live; the oldest goes first.
	first := h.login()
	for i := 0; i < maxSessions; i++ {
		h.clock.advance(time.Second)
		h.login()
	}
	wantError(t, "the oldest of too many sessions", h.send(http.MethodGet, "/api/approvals", nil, first), http.StatusUnauthorized, "LADO_PHONE_LOGIN_REQUIRED")
}

func TestCloneWarningRefusedForever(t *testing.T) {
	h := newApprovalHarness(t)
	h.key.count = 5
	cookie := h.login()
	if stored := h.passkeys()[0]; stored.SignCount != 5 || stored.CloneWarning {
		t.Fatalf("after a login with count 5: %+v", stored)
	}
	// The count goes backwards: another copy of the key signed.
	h.key.count = 3
	b := h.loginBegin()
	if res, c := h.loginFinish(b.Ceremony, h.key.get(t, b.asBegin(), iPhoneFlags)); res != "failed" || c != nil {
		t.Fatalf("login with a lower count = %q", res)
	}
	if stored := h.passkeys()[0]; !stored.CloneWarning {
		t.Fatalf("clone warning not recorded: %+v", stored)
	}
	if got := h.alerts.kinds(); !slices.Contains(got, AlertCloneWarning) {
		t.Fatalf("alerts = %v", got)
	}
	h.audits.last(t, "phone_passkey.clone_warning")
	// Its sessions end and it never logs in again, whatever it counts.
	wantError(t, "a clone-warned passkey's session", h.send(http.MethodGet, "/api/approvals", nil, cookie), http.StatusUnauthorized, "LADO_PHONE_LOGIN_REQUIRED")
	h.key.count = 50
	if b := h.loginBegin(); b.Result != "none" {
		t.Fatalf("begin with only a clone-warned passkey = %q, want none", b.Result)
	}
}
