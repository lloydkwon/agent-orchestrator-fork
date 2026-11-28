package ladophone

// The pace of integration dry runs. Opening an integration, and beginning
// its approval, runs a dry run of approving it, which reads the PRs, their
// checks and their verification again. A session may not have the phone ask
// for that faster than a person reading what it shows would: dryRunBurst at
// once (opening an integration and approving it right away), then one per
// dryRunInterval.

import (
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

const (
	dryRunBurst    = 2
	dryRunInterval = 5 * time.Second
)

// dryRunLimiter paces each session's dry runs (a generic cell rate
// algorithm): next is when a session's allowance is full again.
type dryRunLimiter struct {
	mu   sync.Mutex
	next map[string]time.Time
}

func newDryRunLimiter() *dryRunLimiter {
	return &dryRunLimiter{next: map[string]time.Time{}}
}

// allow reports whether the session may run a dry run at now, and counts it
// when it may.
func (l *dryRunLimiter) allow(session string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, full := range l.next {
		if !full.After(now) {
			delete(l.next, key)
		}
	}
	next := l.next[session]
	if next.Before(now) {
		next = now
	}
	if next.Sub(now) > (dryRunBurst-1)*dryRunInterval {
		return false
	}
	l.next[session] = next.Add(dryRunInterval)
	return true
}

func slowDown() error {
	return apierr.TooManyRequests("LADO_PHONE_SLOW_DOWN", "LADO is still checking this. Wait a few seconds and try again.")
}
