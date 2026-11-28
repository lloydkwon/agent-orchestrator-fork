package ladophone

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

const testSocket = "/home/dev/.ao/lado/phone/phone.sock"

// fakeTailscale answers the tailscale CLI calls the phone service makes. The
// serve status is the next of statuses on each call, the last one repeating,
// so a test can show the status after `serve --bg` took effect.
//
// A stateful fake instead keeps port 8444's target in target: `serve --bg`
// sets it, `serve ... off` clears it, and the status shows it beside the
// other ports.
type fakeTailscale struct {
	mu       sync.Mutex
	self     string
	statuses []string
	failOn   string
	calls    []string

	stateful bool
	target   string
	funnel   bool
	// onServeStatus, when set, runs at each serve status read.
	onServeStatus func()
}

func (f *fakeTailscale) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if call == "serve status --json" && f.onServeStatus != nil {
		f.onServeStatus()
	}
	if f.failOn != "" && strings.HasPrefix(call, f.failOn) {
		return nil, errors.New("tailscale failed")
	}
	if f.stateful {
		switch {
		case strings.HasPrefix(call, "serve --bg --https=8444 "):
			f.target = strings.TrimPrefix(call, "serve --bg --https=8444 ")
		case call == "serve --https=8444 off":
			f.target = ""
		case call == "serve status --json" && f.target == "":
			return []byte(otherPortsJSON), nil
		case call == "serve status --json":
			return []byte(serveJSON(8444, f.target, f.funnel)), nil
		}
	}
	switch call {
	case "status --json":
		return []byte(f.self), nil
	case "serve status --json":
		if len(f.statuses) == 0 {
			return []byte(`{}`), nil
		}
		out := f.statuses[0]
		if len(f.statuses) > 1 {
			f.statuses = f.statuses[1:]
		}
		return []byte(out), nil
	}
	return nil, nil
}

// changes returns the calls that change tailscale's serve config.
func (f *fakeTailscale) changes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "serve ") && c != "serve status --json" {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeTailscale) allCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeTailscale) currentTarget() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.target
}

func selfJSON(state string) string {
	return fmt.Sprintf(`{"BackendState":%q,"Self":{"DNSName":%q,"TailscaleIPs":[%q,%q],"UserID":42},
		"User":{"42":{"ID":42,"LoginName":%q},"7":{"ID":7,"LoginName":"other@example.com"}}}`,
		state, testDNS+".", testSelfV4, testSelfV6, testOwner)
}

// serveJSON is a serve status with one web handler on port.
func serveJSON(port int, proxy string, funnel bool) string {
	allow := ""
	if funnel {
		allow = fmt.Sprintf(`,"AllowFunnel":{"%s:%d":true}`, testDNS, port)
	}
	return fmt.Sprintf(`{"TCP":{"443":{"HTTPS":true},"%d":{"HTTPS":true}},
		"Web":{"%s:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7317"}}},
		"%s:%d":{"Handlers":{"/":{"Proxy":%q}}}}%s}`, port, testDNS, testDNS, port, proxy, allow)
}

const otherPortsJSON = `{"TCP":{"443":{"HTTPS":true},"8443":{"HTTPS":true}},
	"Web":{"box.tail1234.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7317"}}},
	"box.tail1234.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:7690"}}}}}`

func TestLadoTailscaleSelf(t *testing.T) {
	f := &fakeTailscale{self: selfJSON("Running")}
	self, err := Tailscale{Run: f.run}.Self(context.Background())
	if err != nil {
		t.Fatalf("Self: %v", err)
	}
	if self.DNSName != testDNS || self.Login != testOwner {
		t.Fatalf("Self = %+v", self)
	}
	want := []netip.Addr{netip.MustParseAddr(testSelfV4), netip.MustParseAddr(testSelfV6)}
	if len(self.IPs) != 2 || self.IPs[0] != want[0] || self.IPs[1] != want[1] {
		t.Fatalf("IPs = %v, want %v", self.IPs, want)
	}
	for _, out := range []string{selfJSON("Stopped"), `{}`, `not json`, `{"BackendState":"Running","Self":{"DNSName":"","UserID":1}}`} {
		f := &fakeTailscale{self: out}
		if _, err := (Tailscale{Run: f.run}).Self(context.Background()); err == nil {
			t.Errorf("Self(%s) gave no error", out)
		}
	}
}

func TestLadoServeTargetReadsThePort(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, status, want string
	}{
		{"free", otherPortsJSON, ""},
		{"empty", `{}`, ""},
		{"ours", serveJSON(8444, "unix:"+testSocket, false), "unix:" + testSocket},
		{"other proxy", serveJSON(8444, "http://127.0.0.1:9999", false), "http://127.0.0.1:9999"},
		{"tcp forward", `{"TCP":{"8444":{"TCPForward":"127.0.0.1:22"}}}`, "tcp:127.0.0.1:22"},
		{"https without handlers", `{"TCP":{"8444":{"HTTPS":true}}}`, "occupied"},
		{"foreground", `{"Foreground":{"s1":{"TCP":{"8444":{"HTTPS":true}},"Web":{"box.tail1234.ts.net:8444":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:1"}}}}}}}`, "http://127.0.0.1:1"},
	}
	for _, tc := range cases {
		f := &fakeTailscale{statuses: []string{tc.status}}
		got, err := Tailscale{Run: f.run}.ServeTarget(ctx, 8444)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: ServeTarget = %q, want %q", tc.name, got, tc.want)
		}
	}
	// Our socket behind a second path is not ours alone.
	mixed := `{"TCP":{"8444":{"HTTPS":true}},"Web":{"box.tail1234.ts.net:8444":{"Handlers":{
		"/":{"Proxy":"unix:` + testSocket + `"},"/x":{"Proxy":"http://127.0.0.1:2"}}}}}`
	f := &fakeTailscale{statuses: []string{mixed}}
	got, err := Tailscale{Run: f.run}.ServeTarget(ctx, 8444)
	if err != nil || got == "" || got == "unix:"+testSocket {
		t.Fatalf("mixed handlers: ServeTarget = %q, %v; want a foreign description", got, err)
	}
}

func TestLadoServeAppliesWhenFreeAndChecksTheResult(t *testing.T) {
	ctx := context.Background()
	f := &fakeTailscale{statuses: []string{otherPortsJSON, serveJSON(8444, "unix:"+testSocket, false)}}
	if err := (Tailscale{Run: f.run}).ApplyServe(ctx, 8444, testSocket); err != nil {
		t.Fatalf("ApplyServe: %v", err)
	}
	want := "serve --bg --https=8444 unix:" + testSocket
	if got := f.changes(); len(got) != 1 || got[0] != want {
		t.Fatalf("changes = %q, want [%q]", got, want)
	}

	// Already ours: nothing to change.
	f = &fakeTailscale{statuses: []string{serveJSON(8444, "unix:"+testSocket, false)}}
	if err := (Tailscale{Run: f.run}).ApplyServe(ctx, 8444, testSocket); err != nil {
		t.Fatalf("ApplyServe when ours: %v", err)
	}
	if got := f.changes(); len(got) != 0 {
		t.Fatalf("changes when ours = %q", got)
	}

	// tailscale took the command but the port does not point at us.
	f = &fakeTailscale{statuses: []string{otherPortsJSON}}
	if err := (Tailscale{Run: f.run}).ApplyServe(ctx, 8444, testSocket); !errors.Is(err, ErrServeNotApplied) {
		t.Fatalf("ApplyServe with no effect = %v, want ErrServeNotApplied", err)
	}
}

func TestLadoServeRefusesForeignTarget(t *testing.T) {
	ctx := context.Background()
	for name, status := range map[string]string{
		"other proxy": serveJSON(8444, "http://127.0.0.1:9999", false),
		"other unix":  serveJSON(8444, "unix:/tmp/other.sock", false),
		"tcp forward": `{"TCP":{"8444":{"TCPForward":"127.0.0.1:22"}}}`,
	} {
		f := &fakeTailscale{statuses: []string{status}}
		err := Tailscale{Run: f.run}.ApplyServe(ctx, 8444, testSocket)
		if !errors.Is(err, ErrServeTaken) {
			t.Errorf("%s: ApplyServe = %v, want ErrServeTaken", name, err)
		}
		if got := f.changes(); len(got) != 0 {
			t.Errorf("%s: changed serve: %q", name, got)
		}
	}
	f := &fakeTailscale{statuses: []string{serveJSON(8444, "unix:"+testSocket, true)}}
	if err := (Tailscale{Run: f.run}).ApplyServe(ctx, 8444, testSocket); !errors.Is(err, ErrFunnelOn) {
		t.Fatalf("funnel on: ApplyServe = %v, want ErrFunnelOn", err)
	}
	if got := f.changes(); len(got) != 0 {
		t.Fatalf("funnel on: changed serve: %q", got)
	}
	f = &fakeTailscale{statuses: []string{serveJSON(8444, "unix:"+testSocket, true)}}
	on, err := Tailscale{Run: f.run}.FunnelOn(ctx, 8444)
	if err != nil || !on {
		t.Fatalf("FunnelOn = %v, %v; want true", on, err)
	}
	f = &fakeTailscale{failOn: "serve status"}
	if err := (Tailscale{Run: f.run}).ApplyServe(ctx, 8444, testSocket); err == nil {
		t.Fatal("ApplyServe went ahead without reading the serve status")
	}
	if got := f.changes(); len(got) != 0 {
		t.Fatalf("unreadable status: changed serve: %q", got)
	}
}

func TestLadoServeClearOnlyOurs(t *testing.T) {
	ctx := context.Background()
	for name, status := range map[string]string{
		"other proxy": serveJSON(8444, "http://127.0.0.1:9999", false),
		"other unix":  serveJSON(8444, "unix:/tmp/other.sock", false),
		"free":        otherPortsJSON,
	} {
		f := &fakeTailscale{statuses: []string{status}}
		cleared, err := Tailscale{Run: f.run}.ClearServe(ctx, 8444, testSocket)
		if err != nil || cleared {
			t.Errorf("%s: ClearServe = %v, %v; want false, nil", name, cleared, err)
		}
		if got := f.changes(); len(got) != 0 {
			t.Errorf("%s: changed serve: %q", name, got)
		}
	}
	f := &fakeTailscale{statuses: []string{serveJSON(8444, "unix:"+testSocket, false)}}
	cleared, err := Tailscale{Run: f.run}.ClearServe(ctx, 8444, testSocket)
	if err != nil || !cleared {
		t.Fatalf("ours: ClearServe = %v, %v; want true", cleared, err)
	}
	if got := f.changes(); len(got) != 1 || got[0] != "serve --https=8444 off" {
		t.Fatalf("ours: changes = %q", got)
	}
}
