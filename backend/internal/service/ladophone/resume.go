package ladophone

// Reopening phone approval when the daemon starts. The setting outlives the
// daemon and so does tailscale serve (--bg); the socket and the phone's
// sessions do not. Resume reopens the socket only when what turning it on
// checked still holds, and never changes tailscale serve: a port that no
// longer points at the socket, or has funnel on, stays as it is, and so does
// the setting, while the socket stays closed until the developer turns phone
// approval on again.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// AlertResumeRefused is raised when phone approval is on but the daemon did
// not reopen its socket when it started.
const AlertResumeRefused = "phone_approval.resume_refused"

// ErrResumeRefused means phone approval is on but Resume left its socket
// closed.
var ErrResumeRefused = errors.New("phone approval stays closed")

// Resume reopens the socket of phone approval left on by an earlier daemon.
// Off, it does nothing and asks tailscale nothing. On, it checks again what
// Enable checked: Linux, tailscaled running as another uid, this machine
// still the MagicDNS name and owner recorded, the socket in the same place,
// and the serve port pointing at it without funnel. Then it reopens the
// socket behind a gate with this machine's current tailnet addresses. It
// needs no presence proof, since it opens nothing turning it on did not.
// When a check fails, it logs, raises AlertResumeRefused and returns an
// error wrapping ErrResumeRefused; the daemon keeps starting.
func (s *Service) Resume(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings, err := s.store.LadoPhoneSettings(ctx)
	if err != nil {
		return fmt.Errorf("read the phone approval settings: %w", err)
	}
	if !settings.Enabled {
		return nil
	}
	gate, why, err := s.resumeGate(ctx, settings)
	if why == "" {
		if err = s.restartLocked(gate); err != nil {
			why = "its socket could not be opened"
		}
	}
	if why != "" {
		s.log.Warn("lado phone: phone approval is on but stays closed", "reason", why, "error", err)
		s.alert(ctx, AlertResumeRefused, "Phone approval is on but stays closed: "+why+". Turn it on again in Settings.")
		return fmt.Errorf("%w: %s", ErrResumeRefused, why)
	}
	s.log.Info("lado phone: phone approval reopened", "origin", settings.Origin, "owner", settings.OwnerLogin)
	return nil
}

// resumeGate checks what Resume checks before it opens anything and returns
// the gate to open, or why it must not open, with the error behind it when
// there is one. The caller holds s.mu.
func (s *Service) resumeGate(ctx context.Context, settings domain.LadoPhoneSettings) (GateConfig, string, error) {
	if s.goos != "linux" {
		return GateConfig{}, "it works on Linux only", nil
	}
	uid, err := s.tailscaledUID()
	if err != nil {
		return GateConfig{}, "tailscaled's uid is unknown", err
	}
	if uid == s.selfUID() {
		return GateConfig{}, "tailscaled runs as your own user", nil
	}
	self, err := s.ts.Self(ctx)
	if err != nil {
		return GateConfig{}, "tailscale is not running or did not answer", err
	}
	port := settings.ServePort
	if port == 0 {
		port = s.port
	}
	gate := GateConfig{Host: fmt.Sprintf("%s:%d", self.DNSName, port), OwnerLogin: settings.OwnerLogin, SelfIPs: self.IPs}
	switch {
	case self.DNSName != settings.RPID || gate.Origin() != settings.Origin:
		return GateConfig{}, "this machine's MagicDNS name is not the one it was turned on for", nil
	case !strings.EqualFold(self.Login, settings.OwnerLogin):
		return GateConfig{}, "this machine's tailnet owner is not the one who turned it on", nil
	case settings.SocketPath != "" && settings.SocketPath != s.socketPath:
		return GateConfig{}, "its socket is not where it was turned on", nil
	}
	target, err := s.ts.ServeTarget(ctx, port)
	if err != nil {
		return GateConfig{}, "tailscale serve status could not be read", err
	}
	if target != ServeTargetFor(s.socketPath) {
		return GateConfig{}, fmt.Sprintf("tailscale serve port %d no longer points at it", port), nil
	}
	funnel, err := s.ts.FunnelOn(ctx, port)
	if err != nil {
		return GateConfig{}, "tailscale serve status could not be read", err
	}
	if funnel {
		return GateConfig{}, fmt.Sprintf("tailscale funnel is on for port %d", port), nil
	}
	return gate, "", nil
}
