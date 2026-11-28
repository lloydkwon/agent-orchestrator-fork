package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladophone"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

const phoneWiringDNS = "box.tail1234.ts.net"

// phoneWiringTailscale answers the tailscale CLI with this machine's status
// and port 8444 serving target, and records every call.
type phoneWiringTailscale struct {
	mu     sync.Mutex
	target string
	calls  []string
}

func (f *phoneWiringTailscale) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	switch call {
	case "status --json":
		return []byte(`{"BackendState":"Running","Self":{"DNSName":"` + phoneWiringDNS + `.","TailscaleIPs":["100.101.102.103"],"UserID":42},
			"User":{"42":{"LoginName":"dev@example.com"}}}`), nil
	case "serve status --json":
		if f.target == "" {
			return []byte(`{}`), nil
		}
		return []byte(fmt.Sprintf(`{"TCP":{"8444":{"HTTPS":true}},"Web":{"%s:8444":{"Handlers":{"/":{"Proxy":%q}}}}}`, phoneWiringDNS, f.target)), nil
	}
	return nil, errors.New("unexpected tailscale call " + call)
}

func (f *phoneWiringTailscale) allCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func TestWireLadoPhoneStartsOnlyWhenEnabled(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("phone approval's socket needs Linux")
	}
	ctx := context.Background()
	// A short folder, so the socket path fits whatever TMPDIR is.
	dataDir, err := os.MkdirTemp("/tmp", "lado-phone-wiring-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	cfg := config.Config{DataDir: dataDir}
	store := sqlitetest.MustOpenAt(t, t.TempDir())
	ts := &phoneWiringTailscale{}
	deps := ladoPhoneDeps{tailscale: ts.run, tailscaledUID: func() (int, error) { return os.Getuid() + 1, nil }}
	log := slog.New(slog.DiscardHandler)
	socket := ladophone.SocketPath(dataDir)
	socketExists := func() bool {
		_, err := os.Lstat(socket)
		return !errors.Is(err, fs.ErrNotExist)
	}

	// Off: the daemon starts without asking tailscale anything or opening
	// the socket.
	phone := wireLadoPhone(cfg, store, deps, log)
	done := startLadoPhone(ctx, phone, log)
	<-done
	if calls := ts.allCalls(); len(calls) != 0 {
		t.Fatalf("starting with phone approval off asked tailscale %q", calls)
	}
	if st, err := phone.Status(ctx); err != nil || st.Enabled || st.Listening || socketExists() {
		t.Fatalf("status with phone approval off = %+v, %v; socket exists %v", st, err, socketExists())
	}
	stopLadoPhone(phone, done)

	// On, and tailscale serve still points at the socket: the restarted
	// daemon listens again without touching serve.
	at := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	settings := domain.LadoPhoneSettings{
		RPID: phoneWiringDNS, Origin: "https://" + phoneWiringDNS + ":8444", OwnerLogin: "dev@example.com",
		UserHandle: []byte{1, 2, 3}, ServePort: 8444, SocketPath: socket,
	}
	proof := domain.LadoPresenceProof{Nonce: "phone-on-wiring", Action: domain.LadoPresencePhoneApprovalOn, SubjectID: domain.LadoPhoneApprovalSubject, ExpiresAt: at.Add(time.Minute), UsedAt: at}
	event := domain.LadoAuditEvent{At: at, Action: ladophone.AuditEnabled, SubjectKind: "daemon", SubjectID: "phone_approval", Payload: json.RawMessage(`{}`)}
	if _, err := store.EnableLadoPhone(ctx, settings, proof, event); err != nil {
		t.Fatal(err)
	}
	ts.mu.Lock()
	ts.target = ladophone.ServeTargetFor(socket)
	ts.mu.Unlock()
	phone = wireLadoPhone(cfg, store, deps, log)
	done = startLadoPhone(ctx, phone, log)
	<-done
	if st, err := phone.Status(ctx); err != nil || !st.Enabled || !st.Listening || !socketExists() {
		t.Fatalf("status after a restart with phone approval on = %+v, %v; socket exists %v", st, err, socketExists())
	}
	for _, call := range ts.allCalls() {
		if strings.HasPrefix(call, "serve ") && call != "serve status --json" {
			t.Fatalf("resuming changed tailscale serve: %q", call)
		}
	}

	// Shutting down closes the socket and leaves the setting on.
	stopLadoPhone(phone, done)
	if socketExists() {
		t.Fatal("the socket outlived the daemon's shutdown")
	}
	if got, err := store.LadoPhoneSettings(ctx); err != nil || !got.Enabled {
		t.Fatalf("settings after shutdown = %+v, %v; want still on", got, err)
	}
}
