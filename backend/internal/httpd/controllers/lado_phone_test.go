package controllers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladophone"
)

const ladoPhoneCredentialSHA = "4f2c9b1e7a3d5c6b8e0f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f"

// fakeLadoPhone records what the controller asked of phone approval.
type fakeLadoPhone struct {
	mu    sync.Mutex
	calls []string
	proof domain.LadoPresenceProof
	code  string
}

func (f *fakeLadoPhone) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeLadoPhone) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeLadoPhone) Status(context.Context) (ladophone.Status, error) {
	f.record("status")
	return ladophone.Status{Supported: true, Enabled: true, RPID: "box.tail.ts.net", Origin: "https://box.tail.ts.net:8444", ServePort: 8444}, nil
}

func (f *fakeLadoPhone) Enable(_ context.Context, proof domain.LadoPresenceProof) (ladophone.Status, error) {
	f.record("enable")
	f.proof = proof
	return ladophone.Status{Supported: true, Enabled: true, Listening: true}, nil
}

func (f *fakeLadoPhone) Disable(context.Context) (ladophone.Status, error) {
	f.record("disable")
	return ladophone.Status{Supported: true}, nil
}

func (f *fakeLadoPhone) OpenEnrollWindow(context.Context) (ladophone.Status, error) {
	f.record("enroll-window")
	return ladophone.Status{Supported: true, Enabled: true, EnrollWindowExpiresAt: time.Date(2026, 11, 24, 9, 10, 0, 0, time.UTC)}, nil
}

func (f *fakeLadoPhone) LookupPairing(_ context.Context, code string) (ladophone.Pairing, error) {
	f.record("lookup")
	f.code = code
	if code != "1234-5678" {
		return ladophone.Pairing{}, apierr.Invalid("LADO_PHONE_PAIRING_CODE_WRONG", "That code does not match the phone.", nil)
	}
	return ladophone.Pairing{DeviceHint: "iPhone Safari", Login: "dev@example.com", Fingerprint: "4f2c9b1e", CredentialSHA256: ladoPhoneCredentialSHA}, nil
}

func (f *fakeLadoPhone) ConfirmEnroll(_ context.Context, code string, proof domain.LadoPresenceProof) (domain.LadoPhonePasskey, error) {
	f.record("confirm")
	f.code, f.proof = code, proof
	return domain.LadoPhonePasskey{ID: "lpk_1", Fingerprint: "4f2c9b1e", DeviceHint: "iPhone Safari", Login: "dev@example.com", RPID: "box.tail.ts.net"}, nil
}

func (f *fakeLadoPhone) Passkeys(context.Context) ([]domain.LadoPhonePasskey, error) {
	f.record("passkeys")
	return []domain.LadoPhonePasskey{
		{ID: "lpk_1", Fingerprint: "4f2c9b1e", RevokedAt: time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)},
	}, nil
}

func (f *fakeLadoPhone) RevokePasskey(_ context.Context, id string) (bool, error) {
	f.record("revoke " + id)
	return true, nil
}

// refusingPresence refuses every proof, as a request without the desktop
// app's signature is refused.
type refusingPresence struct{}

func (refusingPresence) Verify(string, string, string, []byte, string, string) (domain.LadoPresenceProof, error) {
	return domain.LadoPresenceProof{}, apierr.Forbidden("LADO_PRESENCE_REQUIRED", "Approve this in the AO desktop app.")
}

func newLadoPhoneServer(t *testing.T, phone controllers.LadoPhoneService, presence controllers.LadoPresence) *httptest.Server {
	t.Helper()
	r := chi.NewRouter()
	r.Route("/api/v1", (&controllers.LadoController{Phone: phone, Presence: presence}).Register)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func TestLadoPhoneEnableNeedsPresence(t *testing.T) {
	phone := &fakeLadoPhone{}
	srv := newLadoPhoneServer(t, phone, refusingPresence{})
	if _, status := ladoHeaderRequest(t, srv, "POST", "/api/v1/lado/phone/enable", `{}`, nil); status != http.StatusForbidden {
		t.Fatalf("enable without presence = %d, want 403", status)
	}
	if n := phone.callCount(); n != 0 {
		t.Fatalf("the service was called %d times without presence", n)
	}

	presence := &recordingPresence{}
	srv = newLadoPhoneServer(t, phone, presence)
	body, status := ladoHeaderRequest(t, srv, "POST", "/api/v1/lado/phone/enable", `{}`, map[string]string{"X-LADO-Presence": "v1.x.1.y"})
	if status != http.StatusOK {
		t.Fatalf("enable = %d %s", status, body)
	}
	if presence.action != domain.LadoPresencePhoneApprovalOn || presence.subject != domain.LadoPhoneApprovalSubject ||
		presence.method != "POST" || presence.path != "/api/v1/lado/phone/enable" || string(presence.body) != `{}` {
		t.Fatalf("presence checked %s %s %q for %s %s", presence.method, presence.path, presence.body, presence.action, presence.subject)
	}
	if phone.proof.Action != domain.LadoPresencePhoneApprovalOn {
		t.Fatalf("service got proof %+v", phone.proof)
	}
	var resp controllers.LadoPhoneStatusResponse
	if err := json.Unmarshal(body, &resp); err != nil || !resp.Enabled || !resp.Listening {
		t.Fatalf("enable response %s: %v", body, err)
	}
}

func TestLadoPhonePasskeyEnrollNeedsPresence(t *testing.T) {
	phone := &fakeLadoPhone{}
	sent := `{"code":"1234-5678","credentialSha256":"` + ladoPhoneCredentialSHA + `"}`
	srv := newLadoPhoneServer(t, phone, refusingPresence{})
	if _, status := ladoHeaderRequest(t, srv, "POST", "/api/v1/lado/phone/passkeys", sent, nil); status != http.StatusForbidden {
		t.Fatalf("enroll without presence = %d, want 403", status)
	}
	if n := phone.callCount(); n != 0 {
		t.Fatalf("the service was called %d times without presence", n)
	}

	presence := &recordingPresence{}
	srv = newLadoPhoneServer(t, phone, presence)
	for _, bad := range []string{
		`{"code":"1234-5678"}`,
		`{"code":"1234-5678","credentialSha256":"` + strings.ToUpper(ladoPhoneCredentialSHA) + `"}`,
		`{"code":"1234-5678","credentialSha256":"abc"}`,
		`{"code":"1234-5678","credentialSha256":"` + ladoPhoneCredentialSHA + `","extra":1}`,
	} {
		if _, status := ladoHeaderRequest(t, srv, "POST", "/api/v1/lado/phone/passkeys", bad, nil); status != http.StatusBadRequest {
			t.Errorf("enroll %s = %d, want 400", bad, status)
		}
	}
	if n := phone.callCount(); n != 0 {
		t.Fatalf("the service was called %d times for bad bodies", n)
	}
	body, status := ladoHeaderRequest(t, srv, "POST", "/api/v1/lado/phone/passkeys", sent, map[string]string{"X-LADO-Presence": "v1.x.1.y"})
	if status != http.StatusOK {
		t.Fatalf("enroll = %d %s", status, body)
	}
	if presence.action != domain.LadoPresencePhonePasskeyEnroll || presence.subject != ladoPhoneCredentialSHA || string(presence.body) != sent {
		t.Fatalf("presence checked %q for %s %s", presence.body, presence.action, presence.subject)
	}
	if phone.code != "1234-5678" || phone.proof.SubjectID != ladoPhoneCredentialSHA {
		t.Fatalf("service got code %q proof %+v", phone.code, phone.proof)
	}
	var resp controllers.LadoPhonePasskeyResponse
	if err := json.Unmarshal(body, &resp); err != nil || resp.ID != "lpk_1" || resp.Fingerprint != "4f2c9b1e" {
		t.Fatalf("enroll response %s: %v", body, err)
	}
}

func TestLadoPhoneRoutesWithoutPresence(t *testing.T) {
	phone := &fakeLadoPhone{}
	srv := newLadoPhoneServer(t, phone, refusingPresence{})
	cases := []struct {
		method, path, body string
		want               int
		call               string
	}{
		{"GET", "/api/v1/lado/phone/status", "", http.StatusOK, "status"},
		{"POST", "/api/v1/lado/phone/disable", "", http.StatusOK, "disable"},
		{"POST", "/api/v1/lado/phone/enroll-window", "", http.StatusOK, "enroll-window"},
		{"POST", "/api/v1/lado/phone/pairings/lookup", `{"code":"1234-5678"}`, http.StatusOK, "lookup"},
		{"POST", "/api/v1/lado/phone/pairings/lookup", `{"code":"0000-0000"}`, http.StatusBadRequest, "lookup"},
		{"GET", "/api/v1/lado/phone/passkeys", "", http.StatusOK, "passkeys"},
		{"DELETE", "/api/v1/lado/phone/passkeys/lpk_1", "", http.StatusOK, "revoke lpk_1"},
	}
	for _, c := range cases {
		body, status := ladoHeaderRequest(t, srv, c.method, c.path, c.body, nil)
		if status != c.want {
			t.Errorf("%s %s = %d %s, want %d", c.method, c.path, status, body, c.want)
		}
		phone.mu.Lock()
		last := ""
		if len(phone.calls) > 0 {
			last = phone.calls[len(phone.calls)-1]
		}
		phone.mu.Unlock()
		if last != c.call {
			t.Errorf("%s %s called %q, want %q", c.method, c.path, last, c.call)
		}
	}
	body, _ := ladoHeaderRequest(t, srv, "GET", "/api/v1/lado/phone/passkeys", "", nil)
	var list controllers.LadoPhonePasskeyListResponse
	if err := json.Unmarshal(body, &list); err != nil || len(list.Passkeys) != 1 || !list.Passkeys[0].Revoked || list.Passkeys[0].RevokedAt == nil {
		t.Fatalf("passkeys %s: %v", body, err)
	}
	body, _ = ladoHeaderRequest(t, srv, "POST", "/api/v1/lado/phone/enroll-window", "", nil)
	var st controllers.LadoPhoneStatusResponse
	if err := json.Unmarshal(body, &st); err != nil || st.EnrollWindowExpiresAt == nil {
		t.Fatalf("enroll window %s: %v", body, err)
	}
}

func TestLadoPhoneRoutesWithoutService(t *testing.T) {
	srv := newLadoPhoneServer(t, nil, &recordingPresence{})
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/lado/phone/status", ""},
		{"POST", "/api/v1/lado/phone/enable", `{}`},
		{"POST", "/api/v1/lado/phone/disable", ""},
		{"POST", "/api/v1/lado/phone/enroll-window", ""},
		{"POST", "/api/v1/lado/phone/pairings/lookup", `{"code":"1"}`},
		{"GET", "/api/v1/lado/phone/passkeys", ""},
		{"POST", "/api/v1/lado/phone/passkeys", `{}`},
		{"DELETE", "/api/v1/lado/phone/passkeys/lpk_1", ""},
	} {
		if _, status := ladoHeaderRequest(t, srv, c.method, c.path, c.body, nil); status != http.StatusNotImplemented {
			t.Errorf("%s %s without a service = %d, want 501", c.method, c.path, status)
		}
	}
}
