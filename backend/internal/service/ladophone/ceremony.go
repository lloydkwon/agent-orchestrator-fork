// Portions adapted from mux serve, Copyright (c) 2026 mux contributors, MIT (see LICENSE.mux).

package ladophone

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// WebAuthn ceremonies in progress. The challenge (SessionData) stays in
// memory on the server; the phone gets only an id. Each ceremony is taken
// once, whether it then succeeds or not.

const (
	// ceremonyTTL is how long a phone has between begin and finish.
	ceremonyTTL = 3 * time.Minute
	// maxCeremonies bounds the table; the oldest go first.
	maxCeremonies = 16
)

// ceremonyKind keeps one kind of ceremony from finishing as another: a
// login's assertion never finishes an approval, nor the other way round.
type ceremonyKind string

const (
	ceremonyEnroll  ceremonyKind = "enroll"
	ceremonyLogin   ceremonyKind = "login"
	ceremonyApprove ceremonyKind = "approve"
)

type ceremony struct {
	kind ceremonyKind
	// login is the tailnet login that began it; the finish must come from
	// the same login.
	login string
	// rpID is the relying party it was begun for.
	rpID    string
	data    webauthn.SessionData
	started time.Time

	// An approval ceremony also holds the phone session that began it, the
	// one passkey that may sign it and what it approves.
	session      string
	passkeyID    string
	credentialID []byte
	approval     boundApproval
}

type ceremonyTable struct {
	mu sync.Mutex
	m  map[string]ceremony
}

func newCeremonyTable() *ceremonyTable {
	return &ceremonyTable{m: map[string]ceremony{}}
}

// begin stores c under a new id and returns the id.
func (t *ceremonyTable) begin(c ceremony) string {
	return t.put(randomToken(), c)
}

// put stores c under id, an id the caller made with randomToken because the
// ceremony's challenge names it. Ceremonies older than ceremonyTTL go first,
// then the oldest while the table is full.
func (t *ceremonyTable) put(id string, c ceremony) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	for old, have := range t.m {
		if c.started.Sub(have.started) > ceremonyTTL {
			delete(t.m, old)
		}
	}
	for len(t.m) >= maxCeremonies {
		oldest := ""
		for id, have := range t.m {
			if oldest == "" || have.started.Before(t.m[oldest].started) {
				oldest = id
			}
		}
		delete(t.m, oldest)
	}
	t.m[id] = c
	return id
}

// take removes the ceremony and returns it when it is of kind, was begun by
// login and is younger than ceremonyTTL.
func (t *ceremonyTable) take(id string, kind ceremonyKind, login string, now time.Time) (ceremony, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.m[id]
	if !ok {
		return ceremony{}, false
	}
	delete(t.m, id)
	if c.kind != kind || !strings.EqualFold(c.login, login) || now.Sub(c.started) > ceremonyTTL || now.Before(c.started) {
		return ceremony{}, false
	}
	return c, true
}

// drop removes every ceremony of kind.
func (t *ceremonyTable) drop(kind ceremonyKind) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, c := range t.m {
		if c.kind == kind {
			delete(t.m, id)
		}
	}
}

// dropSession removes every ceremony of kind that the phone session began.
func (t *ceremonyTable) dropSession(kind ceremonyKind, session string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, c := range t.m {
		if c.kind == kind && c.session == session {
			delete(t.m, id)
		}
	}
}

// randomToken is 32 random bytes, base64url.
func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read does not fail (Go 1.24+ aborts instead)
	return base64.RawURLEncoding.EncodeToString(b)
}
