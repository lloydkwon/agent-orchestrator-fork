package ladophone

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// phoneHarness is a phone approval service over a real store, a stateful
// fake tailscale and a fixed clock. tailscaled runs as another uid than the
// test, as on the developer's machine.
type phoneHarness struct {
	t       *testing.T
	s       *Service
	store   *sqlite.Store
	ts      *fakeTailscale
	clock   *testClock
	logs    *syncBuffer
	dataDir string
	state   string
}

func newPhoneHarness(t *testing.T) *phoneHarness {
	t.Helper()
	return newPhoneHarnessWith(t, nil)
}

// newPhoneHarnessWith is newPhoneHarness with edit changing the options
// before the service is made.
func newPhoneHarnessWith(t *testing.T, edit func(*Options)) *phoneHarness {
	t.Helper()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	state := shortTempDir(t)
	ts := &fakeTailscale{self: selfJSON("Running"), stateful: true}
	log, logs := testLogger()
	clock := &testClock{now: time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)}
	opts := Options{
		Store: store, StateDir: state, Tailscale: ts.run, Now: clock.Now, Logger: log,
		TailscaledUID: func() (int, error) { return os.Getuid() + 1, nil },
		goos:          "linux",
	}
	if edit != nil {
		edit(&opts)
	}
	s := New(opts)
	t.Cleanup(s.Close)
	return &phoneHarness{t: t, s: s, store: store, ts: ts, clock: clock, logs: logs, dataDir: dataDir, state: state}
}

func (h *phoneHarness) socket() string { return SocketPath(h.state) }

// desktopProof is a proof as the desktop app's presence check returns it.
func (h *phoneHarness) desktopProof(nonce, action, subject string) domain.LadoPresenceProof {
	now := h.clock.Now()
	return domain.LadoPresenceProof{Nonce: nonce, Action: action, SubjectID: subject, ExpiresAt: now.Add(time.Minute), UsedAt: now}
}

func (h *phoneHarness) enable() Status {
	h.t.Helper()
	st, err := h.s.Enable(context.Background(), h.desktopProof("on-proof-aaaaaaaaaaaa", domain.LadoPresencePhoneApprovalOn, domain.LadoPhoneApprovalSubject))
	if err != nil {
		h.t.Fatalf("Enable: %v\n%s", err, h.logs)
	}
	return st
}

func (h *phoneHarness) settings() domain.LadoPhoneSettings {
	h.t.Helper()
	st, err := h.store.LadoPhoneSettings(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return st
}

func apiCode(err error) string {
	var e *apierr.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func socketExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

func TestEnableRefusesDevProof(t *testing.T) {
	h := newPhoneHarness(t)
	for name, proof := range map[string]domain.LadoPresenceProof{
		"dev":          {Nonce: "dev-1", Action: domain.LadoPresencePhoneApprovalOn, SubjectID: domain.LadoPhoneApprovalSubject, Dev: true},
		"passkey":      {Nonce: "pk-1", Action: domain.LadoPresencePhoneApprovalOn, SubjectID: domain.LadoPhoneApprovalSubject, Via: "passkey:abcd1234"},
		"other action": {Nonce: "other-1", Action: domain.LadoPresencePlanApprove, SubjectID: domain.LadoPhoneApprovalSubject},
	} {
		_, err := h.s.Enable(context.Background(), proof)
		if apiCode(err) != "LADO_PHONE_PROOF_REFUSED" {
			t.Errorf("%s: Enable = %v, want LADO_PHONE_PROOF_REFUSED", name, err)
		}
	}
	if calls := h.ts.allCalls(); len(calls) != 0 {
		t.Fatalf("tailscale was asked %q before the proof was checked", calls)
	}
	if socketExists(h.socket()) || h.settings().Enabled {
		t.Fatal("a refused proof opened the socket or turned phone approval on")
	}
}

func TestEnableRefusesSameUIDTailscaled(t *testing.T) {
	h := newPhoneHarness(t)
	h.s.tailscaledUID = func() (int, error) { return os.Getuid(), nil }
	_, err := h.s.Enable(context.Background(), h.desktopProof("on-proof-bbbbbbbbbbbb", domain.LadoPresencePhoneApprovalOn, domain.LadoPhoneApprovalSubject))
	if apiCode(err) != "LADO_PHONE_TAILSCALED_SAME_USER" {
		t.Fatalf("Enable = %v, want LADO_PHONE_TAILSCALED_SAME_USER", err)
	}
	if changes := h.ts.changes(); len(changes) != 0 {
		t.Fatalf("serve changed: %q", changes)
	}
	if socketExists(h.socket()) || h.settings().Enabled {
		t.Fatal("phone approval came on with tailscaled running as the same user")
	}

	h.s.tailscaledUID = func() (int, error) { return 0, errors.New("no tailscaled") }
	if _, err := h.s.Enable(context.Background(), h.desktopProof("on-proof-cccccccccccc", domain.LadoPresencePhoneApprovalOn, domain.LadoPhoneApprovalSubject)); apiCode(err) != "LADO_PHONE_TAILSCALE_UNAVAILABLE" {
		t.Fatalf("Enable without tailscaled = %v", err)
	}
}

func TestEnableRefusesOffLinux(t *testing.T) {
	h := newPhoneHarness(t)
	h.s.goos = "darwin"
	_, err := h.s.Enable(context.Background(), h.desktopProof("on-proof-dddddddddddd", domain.LadoPresencePhoneApprovalOn, domain.LadoPhoneApprovalSubject))
	if apiCode(err) != "LADO_PHONE_UNSUPPORTED" {
		t.Fatalf("Enable on darwin = %v, want LADO_PHONE_UNSUPPORTED", err)
	}
	if st, err := h.s.Status(context.Background()); err != nil || st.Supported {
		t.Fatalf("Status on darwin = %+v, %v; want unsupported", st, err)
	}
}

func TestEnableRefusesForeignServeAndFunnel(t *testing.T) {
	h := newPhoneHarness(t)
	h.ts.target = "http://127.0.0.1:9999"
	_, err := h.s.Enable(context.Background(), h.desktopProof("on-proof-eeeeeeeeeeee", domain.LadoPresencePhoneApprovalOn, domain.LadoPhoneApprovalSubject))
	if apiCode(err) != "LADO_PHONE_PORT_TAKEN" {
		t.Fatalf("Enable over a foreign target = %v", err)
	}
	if h.ts.currentTarget() != "http://127.0.0.1:9999" || socketExists(h.socket()) || h.settings().Enabled {
		t.Fatal("Enable touched a foreign serve target or came on")
	}

	h.ts.target, h.ts.funnel = "unix:"+h.socket(), true
	_, err = h.s.Enable(context.Background(), h.desktopProof("on-proof-ffffffffffff", domain.LadoPresencePhoneApprovalOn, domain.LadoPhoneApprovalSubject))
	if apiCode(err) != "LADO_PHONE_FUNNEL_ON" {
		t.Fatalf("Enable with funnel on = %v", err)
	}
	if socketExists(h.socket()) || h.settings().Enabled {
		t.Fatal("Enable came on with funnel on")
	}
}

func TestEnableOpensSocketAndServeThenDisableClears(t *testing.T) {
	h := newPhoneHarness(t)
	st := h.enable()
	if !st.Enabled || !st.Listening || st.RPID != testDNS || st.Origin != testOrigin || st.OwnerLogin != testOwner || st.ServePort != 8444 {
		t.Fatalf("status after Enable = %+v", st)
	}
	if st.EnrollURL != testOrigin+"/enroll" {
		t.Fatalf("EnrollURL = %q", st.EnrollURL)
	}
	if h.ts.currentTarget() != "unix:"+h.socket() || !socketExists(h.socket()) {
		t.Fatalf("serve target %q, socket %v", h.ts.currentTarget(), socketExists(h.socket()))
	}
	settings := h.settings()
	if !settings.Enabled || settings.RPID != testDNS || settings.SocketPath != h.socket() || len(settings.UserHandle) != 32 ||
		settings.EnableProofNonce != "on-proof-aaaaaaaaaaaa" {
		t.Fatalf("stored settings = %+v", settings)
	}

	// Turning it on again keeps the user handle, so enrolled passkeys stay
	// valid.
	handle := settings.UserHandle
	if _, err := h.s.Enable(context.Background(), h.desktopProof("on-proof-gggggggggggg", domain.LadoPresencePhoneApprovalOn, domain.LadoPhoneApprovalSubject)); err != nil {
		t.Fatalf("Enable again: %v", err)
	}
	if got := h.settings().UserHandle; string(got) != string(handle) {
		t.Fatal("turning it on again changed the user handle")
	}

	st, err := h.s.Disable(context.Background())
	if err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if st.Enabled || st.Listening {
		t.Fatalf("status after Disable = %+v", st)
	}
	if h.ts.currentTarget() != "" || socketExists(h.socket()) || h.settings().Enabled {
		t.Fatalf("after Disable: target %q, socket %v, enabled %v", h.ts.currentTarget(), socketExists(h.socket()), h.settings().Enabled)
	}
}

func TestDisableLeavesAForeignServeTarget(t *testing.T) {
	h := newPhoneHarness(t)
	h.enable()
	if !socketExists(h.socket()) || h.ts.currentTarget() != "unix:"+h.socket() {
		t.Fatal("Enable did not open the socket and serve")
	}
	// Someone pointed the port elsewhere after phone approval came on.
	h.ts.target = "http://127.0.0.1:9999"
	if _, err := h.s.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if h.ts.currentTarget() != "http://127.0.0.1:9999" {
		t.Fatalf("Disable cleared a foreign target: %q", h.ts.currentTarget())
	}
	if socketExists(h.socket()) || h.settings().Enabled {
		t.Fatal("Disable left the socket or the setting on")
	}
}

func TestEnableRollsBackWhenTheStoreRefuses(t *testing.T) {
	h := newPhoneHarness(t)
	proof := h.desktopProof("on-proof-hhhhhhhhhhhh", domain.LadoPresencePhoneApprovalOn, domain.LadoPhoneApprovalSubject)
	if _, err := h.s.Enable(context.Background(), proof); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if _, err := h.s.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	// The same proof again is a replay; nothing may stay on.
	_, err := h.s.Enable(context.Background(), proof)
	if apiCode(err) != "LADO_PRESENCE_REPLAYED" {
		t.Fatalf("replayed Enable = %v", err)
	}
	if h.ts.currentTarget() != "" || socketExists(h.socket()) || h.settings().Enabled {
		t.Fatalf("after a refused Enable: target %q, socket %v", h.ts.currentTarget(), socketExists(h.socket()))
	}
	if !strings.Contains(h.logs.String(), "lado phone") {
		t.Fatalf("rollback not logged: %s", h.logs)
	}
}
