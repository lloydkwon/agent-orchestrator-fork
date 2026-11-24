// Package ladophone serves LADO's phone approval: approving plans and
// integrations with a passkey on the developer's phone, reached over the
// tailnet (LADO fork; docs/LJY-PRD/TDD.md §1.1).
//
// The phone never reaches the daemon's API. `tailscale serve` forwards one
// HTTPS port to a Unix socket that only tailscaled (another uid) may connect
// to, so a process of the developer's own user, an agent included, cannot
// talk to the socket directly. Every request then passes a gate that checks
// what tailscale serve vouches for, and only the routes this package names
// are served.
package ladophone

import (
	"errors"
	"log/slog"
	"path/filepath"
)

// maxSocketPathBytes is the longest Unix socket path Linux takes: sun_path is
// 108 bytes including the trailing NUL.
const maxSocketPathBytes = 107

// SocketPath is the phone socket's default path under AO's state directory:
// <state>/lado/phone/phone.sock. The socket gets a folder of its own so the
// folder can be owner-only without changing <state>/lado, which the desktop
// app keeps at 0750.
func SocketPath(stateDir string) string {
	return filepath.Join(stateDir, "lado", "phone", "phone.sock")
}

// ListenOptions configures Listen.
type ListenOptions struct {
	// ExpectedUID returns the only uid allowed to connect: tailscaled's.
	// Nil means TailscaledUID. It is asked on every connection, and an error
	// refuses the connection.
	ExpectedUID func() (int, error)
	// Logger receives a warning for each refused connection.
	Logger *slog.Logger

	// selfUID is the daemon's uid, os.Getuid unless a test says otherwise.
	selfUID func() int
}

// ErrUnsupported means phone approval's socket is not available on this
// platform: it needs Linux's SO_PEERCRED.
var ErrUnsupported = errors.New("phone approval needs Linux")

// ErrSocketInUse means another process listens on the phone socket.
var ErrSocketInUse = errors.New("another process listens on the phone approval socket")

// peerAllowed is the accept decision: the peer is the expected uid, and that
// uid is not the daemon's own. A daemon whose tailscaled runs as the same
// user (or a daemon running as root) accepts nobody, because then every
// process of that user could reach the socket.
func peerAllowed(peer, expected, self int) bool {
	return peer == expected && peer != self
}
