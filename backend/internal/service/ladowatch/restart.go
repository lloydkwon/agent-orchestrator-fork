package ladowatch

// LADO fork: the restart message. With the developer's setting on, the
// daemon starts again, at its start, the agents a reboot stopped
// (session_manager/lado_startup_restore.go). A worker whose turn the reboot
// cut off would then sit at its prompt until the stall watch nudges it, long
// after: LADO tells it once, as soon as it rests, to check its state and go
// on. A worker that had finished its turn was waiting, and gets no message.
//
// The restarts are kept in memory: a daemon that stops again before the
// message went sends none, and the stall watch takes over.

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// SessionRestored hears that the daemon started a session's agent again at
// its start. interrupted says the session was in the middle of a turn; only
// such a session is told to go on, and only if it is a running LADO worker.
func (s *Service) SessionRestored(id domain.SessionID, interrupted bool) {
	if !interrupted {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restarts[id] = s.deps.Clock().UTC()
}

func (s *Service) restartedAt(id domain.SessionID) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.restarts[id]
	return at, ok
}

func (s *Service) forgetRestart(id domain.SessionID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.restarts, id)
}

// expireRestarts forgets the restarts older than LadoWakeRestartWindow: of a
// worker that never rested, and of a session that is no running LADO worker.
func (s *Service) expireRestarts(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, at := range s.restarts {
		if now.Sub(at) > domain.LadoWakeRestartWindow {
			delete(s.restarts, id)
		}
	}
}

// watchRestart sends the restart message to a worker the daemon started
// again with its turn cut off: once, while it rests, within
// LadoWakeRestartWindow of the restart, never in answer to a question it
// asked the owner or after a done report, and only where the project lets
// LADO wake its workers. A worker at its usage limit is resumed by the limit
// watch instead. A message that did not go is tried again on the next tick.
func (s *Service) watchRestart(ctx context.Context, w *worker, now time.Time) error {
	at, ok := s.restartedAt(w.session.ID)
	if !ok {
		return nil
	}
	switch {
	case now.Sub(at) > domain.LadoWakeRestartWindow,
		!w.settings.WakeEnabled, s.deps.Messenger == nil,
		w.answering(), w.ownerStopped(),
		w.watch.LimitState == domain.LadoLimitLimited:
		s.forgetRestart(w.session.ID)
		return nil
	case !w.resting():
		return nil
	}
	if err := s.send(ctx, *w, "restart_resume", domain.LadoWakeRestartResumeText, now, map[string]any{"restartedAt": at}); err != nil {
		return err
	}
	s.forgetRestart(w.session.ID)
	s.alert(ctx, *w, domain.LadoAlertRestartResumed, 1, 0, 0, now)
	return nil
}
