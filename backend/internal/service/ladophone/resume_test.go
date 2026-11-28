package ladophone

import (
	"context"
	"net/netip"
	"os"
	"slices"
	"testing"
)

// restart is the daemon starting again over the same store, state folder
// and tailscale: the old service closes (its socket goes, the setting and
// tailscale serve stay) and a new one is made from the same options.
func (h *phoneHarness) restart() *Service {
	h.t.Helper()
	h.s.Close()
	s := New(h.opts)
	h.t.Cleanup(s.Close)
	h.s = s
	return s
}

// newResumeHarness is phone approval turned on with a notifier, then the
// daemon restarted.
func newResumeHarness(t *testing.T) (*phoneHarness, *fakeNotifier) {
	t.Helper()
	alerts := &fakeNotifier{}
	h := newPhoneHarnessWith(t, func(o *Options) { o.Notifier = alerts })
	h.enable()
	h.restart()
	if socketExists(h.socket()) {
		t.Fatal("the socket outlived the closed service")
	}
	return h, alerts
}

func (h *phoneHarness) listening() bool {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	return h.s.server != nil
}

func TestResumeDoesNothingWhenOff(t *testing.T) {
	h := newPhoneHarness(t)
	if err := h.s.Resume(context.Background()); err != nil {
		t.Fatalf("Resume while off: %v", err)
	}
	if calls := h.ts.allCalls(); len(calls) != 0 {
		t.Fatalf("Resume while off asked tailscale %q", calls)
	}
	if socketExists(h.socket()) || h.listening() {
		t.Fatal("Resume while off opened the socket")
	}
}

func TestResumeReopensWhenServeIsOurs(t *testing.T) {
	h, alerts := newResumeHarness(t)
	changes := len(h.ts.changes())
	if err := h.s.Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v\n%s", err, h.logs)
	}
	if !socketExists(h.socket()) || !h.listening() {
		t.Fatal("Resume did not reopen the socket")
	}
	if got := h.ts.changes(); len(got) != changes {
		t.Fatalf("Resume changed tailscale serve: %q", got[changes:])
	}
	h.s.mu.Lock()
	gate := h.s.gate
	h.s.mu.Unlock()
	if gate.Host != testHost || gate.OwnerLogin != testOwner || !slices.Contains(gate.SelfIPs, netip.MustParseAddr(testSelfV4)) {
		t.Fatalf("resumed gate = %+v", gate)
	}
	st, err := h.s.Status(context.Background())
	if err != nil || !st.Enabled || !st.Listening {
		t.Fatalf("Status after Resume = %+v, %v", st, err)
	}
	if kinds := alerts.kinds(); len(kinds) != 0 {
		t.Fatalf("Resume raised %q", kinds)
	}
}

func TestResumeLeavesForeignServeClosed(t *testing.T) {
	for name, edit := range map[string]func(f *fakeTailscale){
		"other proxy": func(f *fakeTailscale) { f.target = "http://127.0.0.1:9999" },
		"other unix":  func(f *fakeTailscale) { f.target = "unix:/tmp/other.sock" },
		"cleared":     func(f *fakeTailscale) { f.target = "" },
		"funnel on":   func(f *fakeTailscale) { f.funnel = true },
	} {
		t.Run(name, func(t *testing.T) {
			h, alerts := newResumeHarness(t)
			h.ts.mu.Lock()
			edit(h.ts)
			target := h.ts.target
			h.ts.mu.Unlock()
			changes := len(h.ts.changes())
			if err := h.s.Resume(context.Background()); err == nil {
				t.Fatal("Resume opened phone approval over a serve port that is not its own")
			}
			if socketExists(h.socket()) || h.listening() {
				t.Fatal("Resume opened the socket")
			}
			if got := h.ts.changes(); len(got) != changes || h.ts.currentTarget() != target {
				t.Fatalf("Resume changed tailscale serve: %q, target %q", got[changes:], h.ts.currentTarget())
			}
			if !h.settings().Enabled {
				t.Fatal("Resume turned the setting off")
			}
			if !slices.Contains(alerts.kinds(), AlertResumeRefused) {
				t.Fatalf("alerts %q, want %s", alerts.kinds(), AlertResumeRefused)
			}
		})
	}
}

func TestResumeRefusesAnotherNodeOrOwner(t *testing.T) {
	for name, self := range map[string]string{
		"node":  `{"BackendState":"Running","Self":{"DNSName":"other.tail1234.ts.net.","TailscaleIPs":["100.64.0.9"],"UserID":42},"User":{"42":{"LoginName":"` + testOwner + `"}}}`,
		"owner": `{"BackendState":"Running","Self":{"DNSName":"` + testDNS + `.","TailscaleIPs":["100.64.0.9"],"UserID":7},"User":{"7":{"LoginName":"other@example.com"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			h, alerts := newResumeHarness(t)
			h.ts.mu.Lock()
			h.ts.self = self
			h.ts.mu.Unlock()
			if err := h.s.Resume(context.Background()); err == nil {
				t.Fatal("Resume opened phone approval for another node or owner")
			}
			if socketExists(h.socket()) || h.listening() {
				t.Fatal("Resume opened the socket")
			}
			if !slices.Contains(alerts.kinds(), AlertResumeRefused) {
				t.Fatalf("alerts %q, want %s", alerts.kinds(), AlertResumeRefused)
			}
		})
	}
}

func TestResumeRefusesSameUIDTailscaled(t *testing.T) {
	h, alerts := newResumeHarness(t)
	h.s.tailscaledUID = func() (int, error) { return os.Getuid(), nil }
	calls := len(h.ts.allCalls())
	if err := h.s.Resume(context.Background()); err == nil {
		t.Fatal("Resume opened phone approval with tailscaled running as the daemon's user")
	}
	if socketExists(h.socket()) || h.listening() {
		t.Fatal("Resume opened the socket")
	}
	if got := h.ts.allCalls(); len(got) != calls {
		t.Fatalf("Resume asked tailscale %q before refusing", got[calls:])
	}
	if !h.settings().Enabled {
		t.Fatal("Resume turned the setting off")
	}
	if !slices.Contains(alerts.kinds(), AlertResumeRefused) {
		t.Fatalf("alerts %q, want %s", alerts.kinds(), AlertResumeRefused)
	}
}
