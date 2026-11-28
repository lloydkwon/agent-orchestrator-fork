package daemon

// LADO fork: phone approval, approving plans and integrations with a passkey
// on the developer's phone over tailscale serve (service/ladophone,
// docs/LJY-PRD/TDD.md §1.1). The daemon builds it, hands it to the loopback
// API, reopens it when it was left on, and closes its socket on shutdown.

import (
	"context"
	"errors"
	"log/slog"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladophone"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladopolicy"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoqueue"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladotask"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoverify"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

var (
	_ ladophone.Store              = (*sqlite.Store)(nil)
	_ ladophone.LadoProjects       = (*sqlite.Store)(nil)
	_ ladophone.Tasks              = (*ladotask.Service)(nil)
	_ ladophone.Rules              = (*ladopolicy.Service)(nil)
	_ ladophone.Executions         = (*ladoqueue.Service)(nil)
	_ ladophone.Integrations       = (*ladoverify.Service)(nil)
	_ controllers.LadoPhoneService = (*ladophone.Service)(nil)
)

// ladoPhoneDeps are the LADO services phone approval reads and approves
// through. A nil one leaves the phone able to log in but listing nothing.
type ladoPhoneDeps struct {
	tasks        ladophone.Tasks
	rules        ladophone.Rules
	executions   ladophone.Executions
	integrations ladophone.Integrations
	// tailscale and tailscaledUID stand in for the tailscale CLI and the
	// owner of tailscaled's socket in tests; nil means the real ones.
	tailscale     mobilebridge.TailscaleRunner
	tailscaledUID func() (int, error)
}

// wireLadoPhone builds phone approval. Its socket is
// <DataDir>/lado/phone/phone.sock and its tailnet port 8444. It starts
// nothing: startLadoPhone reopens it when it was left on.
//
// It has no notifier: AO's notifications are tied to a session or a PR and
// LADO's alerts to a task, and phone approval's alerts are neither, so they
// are logged as warnings, next to the audit events phone approval records.
func wireLadoPhone(cfg config.Config, store *sqlite.Store, deps ladoPhoneDeps, log *slog.Logger) *ladophone.Service {
	return ladophone.New(ladophone.Options{
		Store:         store,
		StateDir:      cfg.DataDir,
		Tailscale:     deps.tailscale,
		TailscaledUID: deps.tailscaledUID,
		Logger:        log,
		Projects:      store,
		Tasks:         deps.tasks,
		Rules:         deps.rules,
		Executions:    deps.executions,
		Integrations:  deps.integrations,
	})
}

// startLadoPhone reopens phone approval when it was left on, in the
// background, since it asks tailscale and must not hold up the daemon's
// start. A refusal is logged; the daemon keeps starting either way.
func startLadoPhone(ctx context.Context, phone *ladophone.Service, log *slog.Logger) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := phone.Resume(ctx)
		if err != nil && !errors.Is(err, ladophone.ErrResumeRefused) {
			log.Warn("LADO could not reopen phone approval", "err", err)
		}
	}()
	return done
}

// stopLadoPhone waits for startLadoPhone, then closes phone approval's socket
// for the daemon's shutdown. The setting and tailscale serve stay, so the
// next daemon reopens it.
func stopLadoPhone(phone *ladophone.Service, started <-chan struct{}) {
	<-started
	phone.Close()
}
