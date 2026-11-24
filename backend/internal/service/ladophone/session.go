// Portions adapted from mux serve, Copyright (c) 2026 mux contributors, MIT (see LICENSE.mux).

package ladophone

// The phone's login sessions and the lockout after failed passkey checks.
// A session only reads: it lists the approvals waiting and opens one. Every
// approval needs its own passkey assertion over what it approves, so the
// cookie alone approves nothing. Sessions live in memory only; restarting
// the daemon logs the phone out.

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"sync"
	"time"
)

const (
	// sessionCookie is the login cookie. The __Host- prefix makes the
	// browser take it only Secure, with Path=/ and no Domain.
	sessionCookie = "__Host-lado-phone"
	// sessionIdle without a request ends a session.
	sessionIdle = 30 * time.Minute
	// maxSessions bounds the table; the oldest go first.
	maxSessions = 32

	// loginMaxFailures failed passkey checks within loginFailureWindow lock
	// logins and approvals for loginLockout.
	loginMaxFailures   = 10
	loginFailureWindow = 10 * time.Minute
	loginLockout       = 5 * time.Minute
)

// phoneSession is one logged-in phone: the passkey that logged it in, the
// tailnet login it came from, and when it was made and last used.
type phoneSession struct {
	// key is the sha256 of the cookie's value; the value itself is kept
	// nowhere on the server.
	key          string
	passkeyID    string
	fingerprint  string
	credentialID []byte
	login        string
	created      time.Time
	lastUsed     time.Time
}

type sessionTable struct {
	mu sync.Mutex
	m  map[string]phoneSession
}

func newSessionTable() *sessionTable {
	return &sessionTable{m: map[string]phoneSession{}}
}

func sessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// open stores a new session and returns the cookie's value.
func (t *sessionTable) open(s phoneSession, now time.Time) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, have := range t.m {
		if now.Sub(have.lastUsed) > sessionIdle {
			delete(t.m, key)
		}
	}
	for len(t.m) >= maxSessions {
		oldest := ""
		for key, have := range t.m {
			if oldest == "" || have.created.Before(t.m[oldest].created) {
				oldest = key
			}
		}
		delete(t.m, oldest)
	}
	token := randomToken()
	s.key, s.created, s.lastUsed = sessionKey(token), now, now
	t.m[s.key] = s
	return token
}

// use returns the session of the cookie's value when it lives and belongs
// to login, and marks it used now. An idle session is removed.
func (t *sessionTable) use(token, login string, now time.Time) (phoneSession, bool) {
	if token == "" {
		return phoneSession{}, false
	}
	key := sessionKey(token)
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.m[key]
	if !ok {
		return phoneSession{}, false
	}
	if now.Sub(s.lastUsed) > sessionIdle || now.Before(s.created) {
		delete(t.m, key)
		return phoneSession{}, false
	}
	if s.login != login {
		return phoneSession{}, false
	}
	s.lastUsed = now
	t.m[key] = s
	return s, true
}

// dropPasskey ends every session the passkey logged in.
func (t *sessionTable) dropPasskey(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, s := range t.m {
		if s.passkeyID == id {
			delete(t.m, key)
		}
	}
}

// clear ends every session.
func (t *sessionTable) clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	clear(t.m)
}

func sessionToken(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// loginGuard counts failed passkey checks and locks logins and approvals
// for a while once there were too many.
type loginGuard struct {
	mu          sync.Mutex
	failures    []time.Time
	lockedUntil time.Time
}

// locked reports whether logins and approvals rest now.
func (g *loginGuard) locked(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return now.Before(g.lockedUntil)
}

// fail counts one failure and reports whether it locked logins just now.
func (g *loginGuard) fail(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failures = slices.DeleteFunc(g.failures, func(at time.Time) bool { return now.Sub(at) >= loginFailureWindow })
	g.failures = append(g.failures, now)
	if len(g.failures) < loginMaxFailures || now.Before(g.lockedUntil) {
		return false
	}
	g.lockedUntil = now.Add(loginLockout)
	g.failures = nil
	return true
}
