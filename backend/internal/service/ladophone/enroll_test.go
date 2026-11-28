package ladophone

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// phone sends one request to the phone socket's handler as tailscale serve
// delivers it from the owner's phone.
func (h *phoneHarness) phone(path string, body any, edit func(*http.Request)) *httptest.ResponseRecorder {
	h.t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		h.t.Fatal(err)
	}
	r := phoneRequest(http.MethodPost, path, func(r *http.Request) {
		r.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1")
		if edit != nil {
			edit(r)
		}
	})
	r.Body = io.NopCloser(strings.NewReader(string(data)))
	r.ContentLength = int64(len(data))
	w := httptest.NewRecorder()
	h.s.phoneHandler(testGateConfig()).ServeHTTP(w, r)
	return w
}

func (h *phoneHarness) begin() beginView {
	h.t.Helper()
	w := h.phone("/enroll/begin", map[string]any{}, nil)
	if w.Code != http.StatusOK {
		h.t.Fatalf("/enroll/begin: %d %s", w.Code, w.Body)
	}
	var b beginView
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		h.t.Fatalf("decode begin: %v", err)
	}
	return b
}

type finishView struct {
	Result      string `json:"result"`
	Code        string `json:"code"`
	Fingerprint string `json:"fingerprint"`
}

func (h *phoneHarness) finish(ceremony string, credential any) finishView {
	h.t.Helper()
	w := h.phone("/enroll/finish", map[string]any{"ceremony": ceremony, "credential": credential}, nil)
	if w.Code != http.StatusOK {
		h.t.Fatalf("/enroll/finish: %d %s", w.Code, w.Body)
	}
	var f finishView
	if err := json.Unmarshal(w.Body.Bytes(), &f); err != nil {
		h.t.Fatalf("decode finish: %v", err)
	}
	return f
}

// pairPhone opens the window and makes a passkey on the phone, returning
// the code and fingerprint the phone shows.
func (h *phoneHarness) pairPhone(k *softKey) finishView {
	h.t.Helper()
	if _, err := h.s.OpenEnrollWindow(context.Background()); err != nil {
		h.t.Fatalf("OpenEnrollWindow: %v", err)
	}
	b := h.begin()
	if b.Result != "ok" {
		h.t.Fatalf("begin: %s", b.Result)
	}
	f := h.finish(b.Ceremony, k.create(h.t, b, iPhoneFlags))
	if f.Result != "ok" {
		h.t.Fatalf("finish: %s\n%s", f.Result, h.logs)
	}
	return f
}

func (h *phoneHarness) enrollProof(nonce, subject string) domain.LadoPresenceProof {
	return h.desktopProof(nonce, domain.LadoPresencePhonePasskeyEnroll, subject)
}

func (h *phoneHarness) passkeys() []domain.LadoPhonePasskey {
	h.t.Helper()
	list, err := h.store.LadoPhonePasskeys(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return list
}

func TestEnrollBeginAsksForAVerifiedResidentPasskey(t *testing.T) {
	h := newPhoneHarness(t)
	h.enable()
	if b := h.begin(); b.Result != "closed" {
		t.Fatalf("begin without an enroll window = %q, want closed", b.Result)
	}
	if _, err := h.s.OpenEnrollWindow(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := h.begin()
	pk := b.Options.PublicKey
	if b.Result != "ok" || b.Ceremony == "" || pk.RP.ID != testDNS || pk.Challenge == "" {
		t.Fatalf("begin = %+v", b)
	}
	if pk.AuthenticatorSelection.UserVerification != "required" || pk.AuthenticatorSelection.ResidentKey != "preferred" {
		t.Fatalf("authenticator selection = %+v", pk.AuthenticatorSelection)
	}
	if pk.Attestation != "none" && pk.Attestation != "" {
		t.Fatalf("attestation = %q", pk.Attestation)
	}
	// The window closes after ten minutes.
	h.clock.advance(10*time.Minute + time.Second)
	if b := h.begin(); b.Result != "closed" {
		t.Fatalf("begin after the window = %q", b.Result)
	}
}

func TestEnrollNeedsDesktopProof(t *testing.T) {
	h := newPhoneHarness(t)
	h.enable()
	k := newSoftKey(t)
	f := h.pairPhone(k)
	if len(f.Code) != 9 || f.Code[4] != '-' || len(f.Fingerprint) != 8 {
		t.Fatalf("phone shows code %q fingerprint %q", f.Code, f.Fingerprint)
	}
	if f.Fingerprint != k.credentialSHA256()[:8] {
		t.Fatalf("fingerprint %q, want the credential's sha256 prefix", f.Fingerprint)
	}
	// The phone alone stores nothing.
	if got := h.passkeys(); len(got) != 0 {
		t.Fatalf("passkeys before the desktop confirmed: %d", len(got))
	}
	pairing, err := h.s.LookupPairing(context.Background(), f.Code)
	if err != nil {
		t.Fatalf("LookupPairing: %v", err)
	}
	if pairing.Fingerprint != f.Fingerprint || pairing.CredentialSHA256 != k.credentialSHA256() ||
		pairing.Login != testOwner || pairing.DeviceHint != "iPhone Safari" || !pairing.CreatedAt.Equal(h.clock.Now()) {
		t.Fatalf("pairing = %+v", pairing)
	}

	// A proof a passkey made, one for another action, or one for another
	// credential does not enroll, and the pairing is gone after any try.
	for name, proof := range map[string]domain.LadoPresenceProof{
		"passkey proof": {Nonce: "pk-enroll-1", Action: domain.LadoPresencePhonePasskeyEnroll, SubjectID: k.credentialSHA256(), Via: "passkey:abcd1234"},
		"other action":  h.desktopProof("enroll-other-1", domain.LadoPresencePhoneApprovalOn, k.credentialSHA256()),
		"other subject": h.enrollProof("enroll-subject-1", strings.Repeat("0", 64)),
	} {
		f := h.pairPhone(newSoftKey(t))
		if _, err := h.s.ConfirmEnroll(context.Background(), f.Code, proof); apiCode(err) != "LADO_PHONE_PROOF_REFUSED" {
			t.Errorf("%s: ConfirmEnroll = %v, want LADO_PHONE_PROOF_REFUSED", name, err)
		}
		if _, err := h.s.LookupPairing(context.Background(), f.Code); err == nil {
			t.Errorf("%s: the pairing survived a refused confirmation", name)
		}
	}
	if got := h.passkeys(); len(got) != 0 {
		t.Fatalf("a refused proof enrolled %d passkeys", len(got))
	}

	f = h.pairPhone(k)
	passkey, err := h.s.ConfirmEnroll(context.Background(), f.Code, h.enrollProof("enroll-proof-aaaaaaaa", k.credentialSHA256()))
	if err != nil {
		t.Fatalf("ConfirmEnroll: %v", err)
	}
	stored := h.passkeys()
	if len(stored) != 1 || stored[0].ID != passkey.ID || stored[0].Fingerprint != f.Fingerprint || stored[0].RPID != testDNS ||
		stored[0].Login != testOwner || stored[0].EnrollProofNonce != "enroll-proof-aaaaaaaa" || string(stored[0].CredentialID) != string(k.id) {
		t.Fatalf("stored passkeys = %+v", stored)
	}
	var cred webauthn.Credential
	if err := json.Unmarshal([]byte(stored[0].CredentialJSON), &cred); err != nil || string(cred.ID) != string(k.id) || len(cred.PublicKey) == 0 {
		t.Fatalf("credential JSON %s: %v", stored[0].CredentialJSON, err)
	}
	if !cred.Flags.UserVerified {
		t.Fatal("the stored credential does not record user verification")
	}
	// The window closed with the enrollment.
	if b := h.begin(); b.Result != "closed" {
		t.Fatalf("begin after an enrollment = %q, want closed", b.Result)
	}
	// The enrolled credential is excluded next time.
	if _, err := h.s.OpenEnrollWindow(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := h.begin()
	if len(b.Options.PublicKey.ExcludeCredentials) != 1 || b.Options.PublicKey.ExcludeCredentials[0].ID != b64(k.id) {
		t.Fatalf("exclusions = %+v", b.Options.PublicKey.ExcludeCredentials)
	}
}

func TestEnrollRefusesDevProof(t *testing.T) {
	h := newPhoneHarness(t)
	h.enable()
	k := newSoftKey(t)
	f := h.pairPhone(k)
	proof := h.enrollProof("dev-enroll-1", k.credentialSHA256())
	proof.Dev = true
	if _, err := h.s.ConfirmEnroll(context.Background(), f.Code, proof); apiCode(err) != "LADO_PHONE_PROOF_REFUSED" {
		t.Fatalf("ConfirmEnroll with a dev proof = %v", err)
	}
	if got := h.passkeys(); len(got) != 0 {
		t.Fatalf("a dev proof enrolled %d passkeys", len(got))
	}
}

func TestEnrollRefusesTwoPendingPairings(t *testing.T) {
	h := newPhoneHarness(t)
	h.enable()
	if _, err := h.s.OpenEnrollWindow(context.Background()); err != nil {
		t.Fatal(err)
	}
	k1, k2 := newSoftKey(t), newSoftKey(t)
	b1, b2 := h.begin(), h.begin()
	f1 := h.finish(b1.Ceremony, k1.create(t, b1, iPhoneFlags))
	if f1.Result != "ok" {
		t.Fatalf("first finish: %s", f1.Result)
	}
	if f2 := h.finish(b2.Ceremony, k2.create(t, b2, iPhoneFlags)); f2.Result != "conflict" || f2.Code != "" {
		t.Fatalf("second finish = %+v, want conflict without a code", f2)
	}
	if _, err := h.s.LookupPairing(context.Background(), f1.Code); err == nil {
		t.Fatal("the first pairing survived a second one")
	}
	if _, err := h.s.ConfirmEnroll(context.Background(), f1.Code, h.enrollProof("enroll-two-1", k1.credentialSHA256())); err == nil {
		t.Fatal("ConfirmEnroll enrolled with two pairings made")
	}
	if b := h.begin(); b.Result != "closed" {
		t.Fatalf("begin after two pairings = %q, want closed", b.Result)
	}
	if got := h.passkeys(); len(got) != 0 {
		t.Fatalf("passkeys = %d", len(got))
	}
}

func TestEnrollLookupClosesAfterFiveWrongCodes(t *testing.T) {
	h := newPhoneHarness(t)
	h.enable()
	k := newSoftKey(t)
	f := h.pairPhone(k)
	wrong := otherCode(f.Code)
	for i := 1; i <= 4; i++ {
		if _, err := h.s.LookupPairing(context.Background(), wrong); apiCode(err) != "LADO_PHONE_PAIRING_CODE_WRONG" {
			t.Fatalf("wrong code %d = %v", i, err)
		}
	}
	// Spaces and dashes do not matter; the right code still works.
	if _, err := h.s.LookupPairing(context.Background(), " "+strings.ReplaceAll(f.Code, "-", " ")+" "); err != nil {
		t.Fatalf("right code after four misses: %v", err)
	}
	if _, err := h.s.LookupPairing(context.Background(), wrong); apiCode(err) != "LADO_PHONE_PAIRING_LOCKED" {
		t.Fatalf("fifth wrong code = %v, want LADO_PHONE_PAIRING_LOCKED", err)
	}
	if _, err := h.s.LookupPairing(context.Background(), f.Code); err == nil {
		t.Fatal("the pairing survived five wrong codes")
	}
	st, err := h.s.Status(context.Background())
	if err != nil || !st.EnrollWindowExpiresAt.IsZero() || st.PairingPending {
		t.Fatalf("status after the lock = %+v, %v", st, err)
	}
	if b := h.begin(); b.Result != "closed" {
		t.Fatalf("begin after the lock = %q", b.Result)
	}
}

func TestEnrollFinishRefusesWithoutUserVerification(t *testing.T) {
	h := newPhoneHarness(t)
	h.enable()
	if _, err := h.s.OpenEnrollWindow(context.Background()); err != nil {
		t.Fatal(err)
	}
	k := newSoftKey(t)
	b := h.begin()
	noUV := protocol.FlagUserPresent | protocol.FlagBackupEligible | protocol.FlagBackupState
	if f := h.finish(b.Ceremony, k.create(t, b, noUV)); f.Result != "failed" || f.Code != "" {
		t.Fatalf("finish without user verification = %+v", f)
	}
	// The ceremony is gone: the same one cannot be finished again.
	if f := h.finish(b.Ceremony, k.create(t, b, iPhoneFlags)); f.Result != "stale" {
		t.Fatalf("second finish of one ceremony = %+v, want stale", f)
	}
	// An assertion is not a registration.
	b = h.begin()
	if f := h.finish(b.Ceremony, k.get(t, b, iPhoneFlags)); f.Result != "failed" {
		t.Fatalf("finish with an assertion = %+v", f)
	}
	// A ceremony older than three minutes is stale.
	b = h.begin()
	h.clock.advance(3*time.Minute + time.Second)
	if f := h.finish(b.Ceremony, k.create(t, b, iPhoneFlags)); f.Result != "stale" {
		t.Fatalf("late finish = %+v, want stale", f)
	}
	// Another origin's response is refused.
	b = h.begin()
	other := newSoftKey(t)
	other.origin = "https://evil.example.com"
	if f := h.finish(b.Ceremony, other.create(t, b, iPhoneFlags)); f.Result != "failed" {
		t.Fatalf("finish from another origin = %+v", f)
	}
}

// TestEnrollCodeNeverPersistedOrLogged walks a whole enrollment with every log
// level on, then looks for the code and its sha256 in the logs, every column
// of every table and every file under the state and data folders.
func TestEnrollCodeNeverPersistedOrLogged(t *testing.T) {
	h := newPhoneHarness(t)
	h.enable()
	k := newSoftKey(t)
	f := h.pairPhone(k)
	digits := strings.ReplaceAll(f.Code, "-", "")
	if _, err := h.s.LookupPairing(context.Background(), f.Code); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.LookupPairing(context.Background(), otherCode(f.Code)); err == nil {
		t.Fatal("a wrong code matched")
	}
	if _, err := h.s.ConfirmEnroll(context.Background(), f.Code, h.enrollProof("enroll-proof-bbbbbbbb", k.credentialSHA256())); err != nil {
		t.Fatalf("ConfirmEnroll: %v", err)
	}
	if len(h.passkeys()) != 1 {
		t.Fatal("not enrolled")
	}
	h.s.Close()

	sum := sha256.Sum256([]byte(digits))
	sumDashed := sha256.Sum256([]byte(f.Code))
	secrets := []string{digits, f.Code, hex.EncodeToString(sum[:]), hex.EncodeToString(sumDashed[:])}
	check := func(where, text string) {
		t.Helper()
		for _, s := range secrets {
			if strings.Contains(text, s) {
				t.Errorf("%s holds %q", where, s)
			}
		}
	}
	check("the log", h.logs.String())
	if !strings.Contains(h.logs.String(), f.Fingerprint) {
		t.Errorf("the log does not name the passkey's fingerprint: %s", h.logs)
	}

	raw, err := sql.Open("sqlite", "file:"+filepath.Join(h.dataDir, "ao.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	tables := sqliteTables(t, raw)
	if len(tables) < 10 {
		t.Fatalf("read only %d tables", len(tables))
	}
	for _, table := range tables {
		for i, row := range sqliteRows(t, raw, table) {
			check(fmt.Sprintf("%s row %d", table, i), row)
		}
	}
	for _, root := range []string{h.state, h.dataDir} {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
				data, err := os.ReadFile(path)
				if err == nil {
					check(path, string(data))
				}
			}
			return nil
		})
	}
}

// otherCode is code with its last digit changed.
func otherCode(code string) string {
	last := code[len(code)-1]
	return code[:len(code)-1] + string(rune('0'+(last-'0'+1)%10))
}

func sqliteTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}

// sqliteRows returns each row of table as text, every column formatted.
func sqliteRows(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`)
	if err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, v := range values {
			if raw, ok := v.([]byte); ok {
				b.Write(raw)
			} else {
				fmt.Fprint(&b, v)
			}
			b.WriteByte('|')
		}
		out = append(out, b.String())
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPasskeysListAndRevoke(t *testing.T) {
	h := newPhoneHarness(t)
	h.enable()
	k := newSoftKey(t)
	f := h.pairPhone(k)
	passkey, err := h.s.ConfirmEnroll(context.Background(), f.Code, h.enrollProof("enroll-proof-cccccccc", k.credentialSHA256()))
	if err != nil {
		t.Fatalf("ConfirmEnroll: %v", err)
	}
	list, err := h.s.Passkeys(context.Background())
	if err != nil || len(list) != 1 || list[0].ID != passkey.ID || list[0].Revoked() {
		t.Fatalf("Passkeys = %+v, %v", list, err)
	}
	if revoked, err := h.s.RevokePasskey(context.Background(), passkey.ID); err != nil || !revoked {
		t.Fatalf("RevokePasskey = %v, %v", revoked, err)
	}
	if revoked, err := h.s.RevokePasskey(context.Background(), passkey.ID); err != nil || revoked {
		t.Fatalf("revoking again = %v, %v; want false", revoked, err)
	}
	if _, err := h.s.RevokePasskey(context.Background(), "lpk_missing"); apiCode(err) != "LADO_PHONE_PASSKEY_NOT_FOUND" {
		t.Fatalf("revoking an unknown passkey = %v", err)
	}
	list, _ = h.s.Passkeys(context.Background())
	if len(list) != 1 || !list[0].Revoked() {
		t.Fatalf("after revoking: %+v", list)
	}
	// A revoked credential stays excluded and cannot be enrolled again.
	if _, err := h.s.OpenEnrollWindow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b := h.begin(); len(b.Options.PublicKey.ExcludeCredentials) != 1 {
		t.Fatalf("exclusions after revoking = %+v", b.Options.PublicKey.ExcludeCredentials)
	}
	f = h.pairPhone(k)
	if _, err := h.s.ConfirmEnroll(context.Background(), f.Code, h.enrollProof("enroll-proof-dddddddd", k.credentialSHA256())); apiCode(err) != "LADO_PHONE_PASSKEY_EXISTS" {
		t.Fatalf("enrolling a revoked credential again = %v", err)
	}
}

func TestOpenEnrollWindowNeedsPhoneApprovalOn(t *testing.T) {
	h := newPhoneHarness(t)
	if _, err := h.s.OpenEnrollWindow(context.Background()); apiCode(err) != "LADO_PHONE_OFF" {
		t.Fatalf("OpenEnrollWindow while off = %v", err)
	}
	h.enable()
	h.s.Close()
	if _, err := h.s.OpenEnrollWindow(context.Background()); apiCode(err) != "LADO_PHONE_NOT_LISTENING" {
		t.Fatalf("OpenEnrollWindow with the socket closed = %v", err)
	}
	if _, err := h.s.LookupPairing(context.Background(), "1234-5678"); apiCode(err) != "LADO_PHONE_ENROLL_CLOSED" {
		t.Fatalf("LookupPairing with no enrollment = %v", err)
	}
	if _, err := h.s.ConfirmEnroll(context.Background(), "1234-5678", h.enrollProof("enroll-closed-1", strings.Repeat("a", 64))); apiCode(err) != "LADO_PHONE_ENROLL_CLOSED" {
		t.Fatalf("ConfirmEnroll with no enrollment = %v", err)
	}
	if _, err := h.s.Enable(context.Background(), h.desktopProof("on-proof-iiiiiiiiiiii", domain.LadoPresencePhoneApprovalOn, domain.LadoPhoneApprovalSubject)); err != nil {
		t.Fatalf("Enable again: %v", err)
	}
	if _, err := h.s.OpenEnrollWindow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.LookupPairing(context.Background(), "1234-5678"); apiCode(err) != "LADO_PHONE_PAIRING_NOT_FOUND" {
		t.Fatalf("LookupPairing before the phone made a passkey = %v", err)
	}
	st, err := h.s.Status(context.Background())
	if err != nil || st.EnrollWindowExpiresAt.IsZero() || !st.EnrollWindowExpiresAt.Equal(h.clock.Now().Add(10*time.Minute)) {
		t.Fatalf("status with an open enrollment = %+v, %v", st, err)
	}
}

func TestDeviceHintUsesFixedWords(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1":    "iPhone Safari",
		"Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36":                      "Android Chrome",
		"Mozilla/5.0 (Linux; Android 14; SM-S918N) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/26.0 Chrome/122.0.0.0 Mobile Safari/537.36": "Android Samsung Internet",
		"Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/131.0 Mobile/15E148 Safari/604.1":              "iPad Chrome",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0; rv:132.0) Gecko/20100101 Firefox/132.0":                                                        "Mac Firefox",
		"<script>alert(1)</script> iPhone": "iPhone",
		"":                                 "unknown device",
		"curl/8.5.0":                       "unknown device",
		"Windows NT 10.0 Edg/131.0 Chrome Safari": "Windows Edge",
	}
	for ua, want := range cases {
		if got := deviceHint(ua); got != want {
			t.Errorf("deviceHint(%q) = %q, want %q", ua, got, want)
		}
	}
}
