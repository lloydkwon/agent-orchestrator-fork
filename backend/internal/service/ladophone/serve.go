package ladophone

// tailscale serve for phone approval's port. This mirrors how AO's Connect
// Mobile reads `tailscale serve status --json` (mobilebridge/tailscaleserve.go),
// but takes the port as an argument and never replaces a target that is not
// phone approval's own socket.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// DefaultServePort is phone approval's tailnet HTTPS port. 443, 8443 and
// 58081 were in use on the developer's machine when it was chosen.
const DefaultServePort = 8444

// tailscaleTimeout bounds every CLI call.
const tailscaleTimeout = 5 * time.Second

var (
	// ErrServeTaken means the port already serves something other than
	// phone approval's socket.
	ErrServeTaken = errors.New("the tailscale serve port is in use by something else")
	// ErrFunnelOn means funnel is on for the port, so the internet could
	// reach it.
	ErrFunnelOn = errors.New("tailscale funnel is on for the phone approval port")
	// ErrServeNotApplied means tailscale took the serve command but the port
	// does not point at the socket afterwards.
	ErrServeNotApplied = errors.New("tailscale serve did not point the port at the phone socket")
	// ErrTailscaleUnavailable means tailscale is not running or its status
	// could not be read.
	ErrTailscaleUnavailable = errors.New("tailscale is not available")
)

// ServeTargetFor is how tailscale serve names a Unix socket target.
func ServeTargetFor(socketPath string) string {
	return "unix:" + socketPath
}

// SelfInfo is what tailscale says about this machine.
type SelfInfo struct {
	// DNSName is the MagicDNS name without the trailing dot.
	DNSName string
	// IPs are the machine's tailnet addresses.
	IPs []netip.Addr
	// Login is the tailnet login of the machine's owner.
	Login string
}

// Tailscale runs the tailscale CLI for phone approval.
type Tailscale struct {
	// Run is the CLI runner; nil means the real tailscale binary.
	Run mobilebridge.TailscaleRunner
}

func (t Tailscale) run(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, tailscaleTimeout)
	defer cancel()
	if t.Run != nil {
		return t.Run(ctx, args...)
	}
	return aoprocess.CommandContext(ctx, "tailscale", args...).Output()
}

// Self reads this machine's MagicDNS name, tailnet addresses and owner login.
// A tailscale that is not running, or a status without them, is
// ErrTailscaleUnavailable.
func (t Tailscale) Self(ctx context.Context) (SelfInfo, error) {
	out, err := t.run(ctx, "status", "--json")
	if err != nil {
		return SelfInfo{}, fmt.Errorf("%w: %w", ErrTailscaleUnavailable, err)
	}
	var parsed struct {
		BackendState string
		Self         *struct {
			DNSName      string
			TailscaleIPs []string
			UserID       int64
		}
		User map[string]struct{ LoginName string }
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return SelfInfo{}, fmt.Errorf("%w: read status: %w", ErrTailscaleUnavailable, err)
	}
	if parsed.BackendState != "Running" || parsed.Self == nil {
		return SelfInfo{}, fmt.Errorf("%w: not running", ErrTailscaleUnavailable)
	}
	self := SelfInfo{
		DNSName: strings.ToLower(strings.TrimSuffix(parsed.Self.DNSName, ".")),
		Login:   parsed.User[strconv.FormatInt(parsed.Self.UserID, 10)].LoginName,
	}
	for _, ip := range parsed.Self.TailscaleIPs {
		if addr, err := netip.ParseAddr(ip); err == nil {
			self.IPs = append(self.IPs, addr)
		}
	}
	if self.DNSName == "" || self.Login == "" || len(self.IPs) == 0 {
		return SelfInfo{}, fmt.Errorf("%w: no MagicDNS name, address or owner", ErrTailscaleUnavailable)
	}
	return self, nil
}

// ServeTarget returns what tailscale serve forwards port to: the proxy target
// of its one handler ("unix:/path" or "http://..."), "tcp:<target>" for a TCP
// forward, another description when the port serves anything else, and ""
// when nothing uses the port.
func (t Tailscale) ServeTarget(ctx context.Context, port int) (string, error) {
	st, err := t.serveStatus(ctx)
	if err != nil {
		return "", err
	}
	return st.target(port), nil
}

// FunnelOn reports whether funnel is on for port.
func (t Tailscale) FunnelOn(ctx context.Context, port int) (bool, error) {
	st, err := t.serveStatus(ctx)
	if err != nil {
		return false, err
	}
	return st.funnel(port), nil
}

// ApplyServe points port at the socket, unless the port serves something
// else (ErrServeTaken) or funnel is on for it (ErrFunnelOn). It reads the
// status again afterwards and returns ErrServeNotApplied when the port does
// not point at the socket.
func (t Tailscale) ApplyServe(ctx context.Context, port int, socketPath string) error {
	st, err := t.serveStatus(ctx)
	if err != nil {
		return err
	}
	ours := ServeTargetFor(socketPath)
	switch target := st.target(port); {
	case st.funnel(port):
		return ErrFunnelOn
	case target == ours:
		return nil
	case target != "":
		return fmt.Errorf("%w: port %d", ErrServeTaken, port)
	}
	if _, err := t.run(ctx, "serve", "--bg", fmt.Sprintf("--https=%d", port), ours); err != nil {
		return fmt.Errorf("tailscale serve --https=%d: %w", port, err)
	}
	after, err := t.ServeTarget(ctx, port)
	if err != nil {
		return err
	}
	if after != ours {
		return ErrServeNotApplied
	}
	return nil
}

// ClearServe turns port's serve off only when it points at the socket, and
// reports whether it did. Another target is left alone.
func (t Tailscale) ClearServe(ctx context.Context, port int, socketPath string) (bool, error) {
	target, err := t.ServeTarget(ctx, port)
	if err != nil {
		return false, err
	}
	if target != ServeTargetFor(socketPath) {
		return false, nil
	}
	if _, err := t.run(ctx, "serve", fmt.Sprintf("--https=%d", port), "off"); err != nil {
		return false, fmt.Errorf("tailscale serve --https=%d off: %w", port, err)
	}
	return true, nil
}

// serveConfig is the part of `tailscale serve status --json` phone approval
// reads. Foreground holds the configs of `tailscale serve` runs without
// --bg, which hold their ports too.
type serveConfig struct {
	TCP map[string]struct {
		HTTPS      bool
		HTTP       bool
		TCPForward string
	}
	Web map[string]struct {
		Handlers map[string]struct {
			Proxy string
			Path  string
			Text  string
		}
	}
	AllowFunnel map[string]bool
	Foreground  map[string]serveConfig
}

func (t Tailscale) serveStatus(ctx context.Context) (serveConfig, error) {
	out, err := t.run(ctx, "serve", "status", "--json")
	if err != nil {
		return serveConfig{}, fmt.Errorf("tailscale serve status: %w", err)
	}
	var st serveConfig
	if err := json.Unmarshal(out, &st); err != nil {
		return serveConfig{}, fmt.Errorf("read tailscale serve status: %w", err)
	}
	return st, nil
}

// target describes what uses port across this config and its foreground
// ones (see ServeTarget).
func (c serveConfig) target(port int) string {
	uses := c.uses(port)
	switch len(uses) {
	case 0:
		return ""
	case 1:
		return uses[0]
	}
	sort.Strings(uses)
	return "several: " + strings.Join(uses, ", ")
}

// uses lists each thing that uses port: a web handler's target, a TCP
// forward, or "occupied" for a listener with no handler.
func (c serveConfig) uses(port int) []string {
	key := strconv.Itoa(port)
	suffix := ":" + key
	var uses []string
	for _, fg := range c.Foreground {
		uses = append(uses, fg.uses(port)...)
	}
	handlers := 0
	for host, site := range c.Web {
		if !strings.HasSuffix(host, suffix) {
			continue
		}
		for path, h := range site.Handlers {
			handlers++
			switch {
			case path == "/" && h.Proxy != "":
				uses = append(uses, h.Proxy)
			case h.Proxy != "":
				uses = append(uses, path+" "+h.Proxy)
			default:
				uses = append(uses, path+" (file or text)")
			}
		}
	}
	if tcp, ok := c.TCP[key]; ok {
		switch {
		case tcp.TCPForward != "":
			uses = append(uses, "tcp:"+tcp.TCPForward)
		case handlers == 0:
			uses = append(uses, "occupied")
		}
	}
	return uses
}

// funnel reports whether funnel is on for port in this config or a
// foreground one.
func (c serveConfig) funnel(port int) bool {
	suffix := ":" + strconv.Itoa(port)
	for hostPort, on := range c.AllowFunnel {
		if on && strings.HasSuffix(hostPort, suffix) {
			return true
		}
	}
	for _, fg := range c.Foreground {
		if fg.funnel(port) {
			return true
		}
	}
	return false
}
