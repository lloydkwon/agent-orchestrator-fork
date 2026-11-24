package ladophone

// The phone approval service: turning it on and off, the socket's HTTP server
// and the enrolled passkeys. Turning it on needs the desktop app's presence
// proof (phone_approval.on); turning it off and revoking a passkey are the
// safe direction and need none, as for Claude in Chrome.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladopresence"
)

// Audit actions of phone approval.
const (
	AuditEnabled        = "phone_approval.enabled"
	AuditDisabled       = "phone_approval.disabled"
	AuditPasskeyEnroll  = "phone_passkey.enrolled"
	AuditPasskeyRevoked = "phone_passkey.revoked"
)

// userHandleBytes is the length of the WebAuthn user handle made when phone
// approval is first turned on.
const userHandleBytes = 32

// Store is the storage phone approval needs; the SQLite store implements it.
type Store interface {
	LadoPhoneSettings(ctx context.Context) (domain.LadoPhoneSettings, error)
	EnableLadoPhone(ctx context.Context, settings domain.LadoPhoneSettings, proof domain.LadoPresenceProof, event domain.LadoAuditEvent) (domain.LadoPhoneSettings, error)
	DisableLadoPhone(ctx context.Context, event domain.LadoAuditEvent) (domain.LadoPhoneSettings, bool, error)
	InsertLadoPhonePasskey(ctx context.Context, passkey domain.LadoPhonePasskey, proof domain.LadoPresenceProof, event domain.LadoAuditEvent) error
	LadoPhonePasskeys(ctx context.Context) ([]domain.LadoPhonePasskey, error)
	RevokeLadoPhonePasskey(ctx context.Context, id string, event domain.LadoAuditEvent) (bool, error)
	LadoPhonePasskeyByCredentialID(ctx context.Context, credentialID []byte) (domain.LadoPhonePasskey, bool, error)
	RecordLadoPhonePasskeyUse(ctx context.Context, id string, signCount uint32, cloneWarning bool, at time.Time) error
	AppendLadoAuditEvent(ctx context.Context, ev domain.LadoAuditEvent) (domain.LadoAuditEvent, error)
}

// Options configures New.
type Options struct {
	Store Store
	// StateDir is AO's state directory; the socket defaults to
	// SocketPath(StateDir).
	StateDir string
	// SocketPath overrides the socket's path.
	SocketPath string
	// Port is the tailnet HTTPS port; 0 means DefaultServePort.
	Port int
	// Tailscale runs the tailscale CLI; nil means the real binary.
	Tailscale mobilebridge.TailscaleRunner
	// TailscaledUID returns the uid allowed on the socket; nil means
	// TailscaledUID.
	TailscaledUID func() (int, error)
	Now           func() time.Time
	Logger        *slog.Logger

	// Projects, Tasks, Rules, Executions and Integrations are where the
	// approvals waiting come from and where an approval goes. Without them
	// the phone can log in but lists and approves nothing.
	Projects     LadoProjects
	Tasks        Tasks
	Rules        Rules
	Executions   Executions
	Integrations Integrations
	// Notifier raises alerts on the desktop; nil only logs them.
	Notifier Notifier

	// goos and selfUID stand in for runtime.GOOS and os.Getuid in tests.
	goos    string
	selfUID func() int
}

// Service is phone approval.
type Service struct {
	store         Store
	ts            Tailscale
	tailscaledUID func() (int, error)
	now           func() time.Time
	log           *slog.Logger
	socketPath    string
	port          int
	goos          string
	selfUID       func() int

	// mu serializes turning on and off and guards the server and its gate.
	mu       sync.Mutex
	server   *http.Server
	listener net.Listener
	gate     GateConfig

	ceremonies *ceremonyTable
	enroll     *enrollment
	sessions   *sessionTable
	guard      *loginGuard
	dryRuns    *dryRunLimiter

	projects     LadoProjects
	tasks        Tasks
	rules        Rules
	executions   Executions
	integrations Integrations
	notifier     Notifier
}

// Status is phone approval's state for the desktop app.
type Status struct {
	// Supported is false off Linux, where phone approval cannot be turned on.
	Supported bool
	Enabled   bool
	// RPID, Origin, OwnerLogin and ServePort are what turning it on fixed;
	// they stay after it is turned off.
	RPID       string
	Origin     string
	OwnerLogin string
	ServePort  int
	// Listening says whether the socket's server runs in this daemon.
	Listening bool
	// EnrollURL is the page a phone opens to make a passkey.
	EnrollURL string
	// EnrollWindowExpiresAt is when the open enrollment closes, zero when
	// none is open.
	EnrollWindowExpiresAt time.Time
	// PairingPending says a phone made a passkey that waits for its code in
	// the desktop app.
	PairingPending bool
}

// New returns the service. It starts nothing; Enable opens the socket.
func New(opts Options) *Service {
	s := &Service{
		store:         opts.Store,
		ts:            Tailscale{Run: opts.Tailscale},
		tailscaledUID: opts.TailscaledUID,
		now:           opts.Now,
		log:           opts.Logger,
		socketPath:    opts.SocketPath,
		port:          opts.Port,
		goos:          opts.goos,
		selfUID:       opts.selfUID,
		ceremonies:    newCeremonyTable(),
		sessions:      newSessionTable(),
		guard:         &loginGuard{},
		dryRuns:       newDryRunLimiter(),
		projects:      opts.Projects,
		tasks:         opts.Tasks,
		rules:         opts.Rules,
		executions:    opts.Executions,
		integrations:  opts.Integrations,
		notifier:      opts.Notifier,
	}
	if s.tailscaledUID == nil {
		s.tailscaledUID = TailscaledUID
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.socketPath == "" {
		s.socketPath = SocketPath(opts.StateDir)
	}
	if s.port == 0 {
		s.port = DefaultServePort
	}
	if s.goos == "" {
		s.goos = runtime.GOOS
	}
	if s.selfUID == nil {
		s.selfUID = os.Getuid
	}
	s.enroll = &enrollment{ceremonies: s.ceremonies}
	return s
}

// Status returns phone approval's state.
func (s *Service) Status(ctx context.Context) (Status, error) {
	settings, err := s.store.LadoPhoneSettings(ctx)
	if err != nil {
		s.log.Error("lado phone: read settings", "error", err)
		return Status{}, apierr.Internal("LADO_PHONE_READ_FAILED", "Failed to read the phone approval settings")
	}
	return s.status(settings), nil
}

func (s *Service) status(settings domain.LadoPhoneSettings) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusUnlocked(settings)
}

// Enable turns phone approval on with the desktop app's proof of
// domain.LadoPresencePhoneApprovalOn. It refuses off Linux, when tailscaled
// runs as the daemon's own user, when the serve port serves something else
// or funnel is on for it. It fixes the relying party (the MagicDNS name),
// the origin and the owner from tailscale, opens the socket, points the
// serve port at it and records it all; when recording fails, it undoes what
// this call changed.
func (s *Service) Enable(ctx context.Context, proof domain.LadoPresenceProof) (Status, error) {
	if !desktopProof(proof, domain.LadoPresencePhoneApprovalOn) {
		return Status{}, proofRefused()
	}
	if s.goos != "linux" {
		return Status{}, apierr.Unavailable("LADO_PHONE_UNSUPPORTED", "Phone approval works on Linux only.")
	}
	uid, err := s.tailscaledUID()
	if err != nil {
		s.log.Warn("lado phone: tailscaled's uid is unknown", "error", err)
		return Status{}, tailscaleUnavailable()
	}
	if uid == s.selfUID() {
		return Status{}, apierr.Forbidden("LADO_PHONE_TAILSCALED_SAME_USER",
			"tailscaled runs as your own user, so any of your programs could reach the phone socket. Phone approval needs tailscaled running as root.")
	}
	self, err := s.ts.Self(ctx)
	if err != nil {
		s.log.Warn("lado phone: read tailscale status", "error", err)
		return Status{}, tailscaleUnavailable()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	before, err := s.ts.ServeTarget(ctx, s.port)
	if err != nil {
		s.log.Warn("lado phone: read tailscale serve status", "error", err)
		return Status{}, tailscaleUnavailable()
	}
	if err := s.serveRefusal(ctx, before); err != nil {
		return Status{}, err
	}
	current, err := s.store.LadoPhoneSettings(ctx)
	if err != nil {
		s.log.Error("lado phone: read settings", "error", err)
		return Status{}, apierr.Internal("LADO_PHONE_READ_FAILED", "Failed to read the phone approval settings")
	}
	handle := current.UserHandle
	if len(handle) == 0 {
		handle = make([]byte, userHandleBytes)
		_, _ = rand.Read(handle)
	}
	gate := GateConfig{Host: fmt.Sprintf("%s:%d", self.DNSName, s.port), OwnerLogin: self.Login, SelfIPs: self.IPs}
	hadServer := s.server != nil
	previous := s.gate
	if err := s.restartLocked(gate); err != nil {
		s.log.Error("lado phone: open the socket", "path", s.socketPath, "error", err)
		return Status{}, apierr.Internal("LADO_PHONE_SOCKET_FAILED", "Failed to open the phone approval socket")
	}
	undo := func() {
		s.stopLocked()
		if hadServer {
			if err := s.restartLocked(previous); err != nil {
				s.log.Error("lado phone: reopen the socket", "error", err)
			}
		}
		if before == "" {
			if _, err := s.ts.ClearServe(ctx, s.port, s.socketPath); err != nil {
				s.log.Warn("lado phone: undo tailscale serve", "error", err)
			}
		}
	}
	if err := s.ts.ApplyServe(ctx, s.port, s.socketPath); err != nil {
		s.log.Warn("lado phone: point tailscale serve at the socket", "port", s.port, "error", err)
		undo()
		return Status{}, s.serveError(err)
	}
	settings := domain.LadoPhoneSettings{
		RPID: self.DNSName, Origin: gate.Origin(), OwnerLogin: self.Login, UserHandle: handle,
		ServePort: s.port, SocketPath: s.socketPath,
	}
	stored, err := s.store.EnableLadoPhone(ctx, settings, proof, s.event(AuditEnabled, "daemon", "phone_approval", map[string]any{
		"rpId": settings.RPID, "origin": settings.Origin, "ownerLogin": settings.OwnerLogin,
		"servePort": settings.ServePort, "proof": proof.Nonce,
	}))
	if err != nil {
		s.log.Warn("lado phone: turning on was not recorded; undoing it", "error", err)
		undo()
		return Status{}, s.storeError(err)
	}
	s.enroll.close()
	s.endSessions()
	s.log.Info("lado phone: phone approval on", "origin", settings.Origin, "owner", settings.OwnerLogin)
	return s.statusUnlocked(stored), nil
}

// Disable turns phone approval off: the socket closes, the enrollment
// closes, the setting is recorded off and the serve port is cleared when it
// still points at the socket. It needs no proof.
func (s *Service) Disable(ctx context.Context) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	s.enroll.close()
	s.endSessions()
	stored, changed, err := s.store.DisableLadoPhone(ctx, s.event(AuditDisabled, "daemon", "phone_approval", map[string]any{}))
	if err != nil {
		s.log.Error("lado phone: record turning off", "error", err)
		return Status{}, apierr.Internal("LADO_PHONE_SAVE_FAILED", "Failed to turn phone approval off")
	}
	port, socket := stored.ServePort, stored.SocketPath
	if port == 0 {
		port = s.port
	}
	if socket == "" {
		socket = s.socketPath
	}
	cleared, err := s.ts.ClearServe(ctx, port, socket)
	if err != nil {
		s.log.Warn("lado phone: clear tailscale serve", "port", port, "error", err)
	}
	if changed {
		s.log.Info("lado phone: phone approval off", "serve_cleared", cleared)
	}
	return s.statusUnlocked(stored), nil
}

// Close stops the socket's server, for the daemon's shutdown. It changes
// neither the setting nor tailscale serve.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	s.enroll.close()
	s.endSessions()
}

// endSessions logs every phone out and drops the logins and approvals in
// progress.
func (s *Service) endSessions() {
	s.sessions.clear()
	s.ceremonies.drop(ceremonyLogin)
	s.ceremonies.drop(ceremonyApprove)
}

// Passkeys returns every enrolled passkey, revoked ones too.
func (s *Service) Passkeys(ctx context.Context) ([]domain.LadoPhonePasskey, error) {
	list, err := s.store.LadoPhonePasskeys(ctx)
	if err != nil {
		s.log.Error("lado phone: read passkeys", "error", err)
		return nil, apierr.Internal("LADO_PHONE_READ_FAILED", "Failed to read the phone passkeys")
	}
	return list, nil
}

// RevokePasskey revokes a passkey and reports whether this revoked it. It
// needs no proof.
func (s *Service) RevokePasskey(ctx context.Context, id string) (bool, error) {
	revoked, err := s.store.RevokeLadoPhonePasskey(ctx, id, s.event(AuditPasskeyRevoked, "phone_passkey", id, map[string]any{}))
	if err == nil {
		// Its sessions end even when it was revoked already.
		s.sessions.dropPasskey(id)
	}
	if errors.Is(err, domain.ErrLadoPhonePasskeyNotFound) {
		return false, apierr.NotFound("LADO_PHONE_PASSKEY_NOT_FOUND", "No such phone passkey")
	}
	if err != nil {
		s.log.Error("lado phone: revoke a passkey", "id", id, "error", err)
		return false, apierr.Internal("LADO_PHONE_SAVE_FAILED", "Failed to revoke the phone passkey")
	}
	if revoked {
		s.log.Info("lado phone: revoked a passkey", "id", id)
	}
	return revoked, nil
}

// serveRefusal refuses a serve port that serves something other than the
// socket, or has funnel on.
func (s *Service) serveRefusal(ctx context.Context, target string) error {
	if target != "" && target != ServeTargetFor(s.socketPath) {
		return s.serveError(ErrServeTaken)
	}
	funnel, err := s.ts.FunnelOn(ctx, s.port)
	if err != nil {
		s.log.Warn("lado phone: read tailscale serve status", "error", err)
		return tailscaleUnavailable()
	}
	if funnel {
		return s.serveError(ErrFunnelOn)
	}
	return nil
}

func (s *Service) serveError(err error) error {
	switch {
	case errors.Is(err, ErrServeTaken):
		return apierr.Conflict("LADO_PHONE_PORT_TAKEN",
			fmt.Sprintf("Tailscale serve port %d already serves something else. Free it or choose another port.", s.port),
			map[string]any{"port": s.port})
	case errors.Is(err, ErrFunnelOn):
		return apierr.Conflict("LADO_PHONE_FUNNEL_ON",
			fmt.Sprintf("Tailscale funnel is on for port %d. Turn it off first.", s.port), map[string]any{"port": s.port})
	}
	return apierr.Unavailable("LADO_PHONE_SERVE_FAILED", "Tailscale serve could not point the port at LADO")
}

func (s *Service) storeError(err error) error {
	switch {
	case errors.Is(err, domain.ErrLadoPresenceReplayed):
		return ladopresence.Replayed()
	case errors.Is(err, domain.ErrLadoPhoneProofRefused):
		return proofRefused()
	case errors.Is(err, domain.ErrLadoPhoneApprovalOff):
		return phoneOff()
	case errors.Is(err, domain.ErrLadoPhonePasskeyExists):
		return apierr.Conflict("LADO_PHONE_PASSKEY_EXISTS", "This passkey is enrolled already.", nil)
	}
	return apierr.Internal("LADO_PHONE_SAVE_FAILED", "Failed to save phone approval")
}

// event is an audit event at the service's time; the store fills in the
// actor.
func (s *Service) event(action, kind, subject string, payload map[string]any) domain.LadoAuditEvent {
	encoded, _ := json.Marshal(payload)
	return domain.LadoAuditEvent{At: s.now().UTC(), Action: action, SubjectKind: kind, SubjectID: subject, Payload: encoded}
}

// statusUnlocked is status for a caller that holds s.mu.
func (s *Service) statusUnlocked(settings domain.LadoPhoneSettings) Status {
	st := Status{
		Supported:  s.goos == "linux",
		Enabled:    settings.Enabled,
		RPID:       settings.RPID,
		Origin:     settings.Origin,
		OwnerLogin: settings.OwnerLogin,
		ServePort:  settings.ServePort,
		Listening:  s.server != nil,
	}
	if settings.Enabled && settings.Origin != "" {
		st.EnrollURL = settings.Origin + "/enroll"
	}
	st.EnrollWindowExpiresAt, st.PairingPending = s.enroll.state(s.now())
	return st
}

// desktopProof reports whether proof is the desktop app's proof of action:
// not a development proof and not one a passkey made. The store checks the
// same again.
func desktopProof(proof domain.LadoPresenceProof, action string) bool {
	return proof.Action == action && !proof.Dev && proof.Via == ""
}

func proofRefused() error {
	return apierr.Forbidden("LADO_PHONE_PROOF_REFUSED", "This needs the confirmation of the AO desktop app.")
}

func phoneOff() error {
	return apierr.Conflict("LADO_PHONE_OFF", "Phone approval is off.", nil)
}

func tailscaleUnavailable() error {
	return apierr.Unavailable("LADO_PHONE_TAILSCALE_UNAVAILABLE", "Tailscale is not running or did not answer.")
}
