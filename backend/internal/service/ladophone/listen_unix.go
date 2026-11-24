//go:build linux

package ladophone

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// tailscaledSocket is tailscaled's control socket. Its owner is the uid
// tailscaled runs as, which is the uid its serve proxy connects with.
const tailscaledSocket = "/var/run/tailscale/tailscaled.sock"

// TailscaledUID returns the uid that owns tailscaled's control socket.
func TailscaledUID() (int, error) {
	info, err := os.Stat(tailscaledSocket)
	if err != nil {
		return 0, fmt.Errorf("read tailscaled's socket: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("read tailscaled's socket owner")
	}
	return int(st.Uid), nil
}

// Listen opens the phone socket at path: its folder owner-only (0700), a
// socket left by an earlier daemon removed, the socket itself 0600. The
// listener's Accept hands over only connections whose peer is the expected
// uid and not the daemon's own; it closes the others and logs a warning.
// Closing the listener removes the socket.
func Listen(path string, opts ListenOptions) (net.Listener, error) {
	if len(path) > maxSocketPathBytes {
		return nil, fmt.Errorf("phone socket path is %d bytes; the maximum is %d", len(path), maxSocketPathBytes)
	}
	if err := ownerOnlyDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on the phone socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("restrict the phone socket: %w", err)
	}
	expected := opts.ExpectedUID
	if expected == nil {
		expected = TailscaledUID
	}
	self := opts.selfUID
	if self == nil {
		self = os.Getuid
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &peerListener{UnixListener: ln, expected: expected, self: self, log: log}, nil
}

// ownerOnlyDir creates dir if needed and makes it 0700, refusing one that is
// a symlink or belongs to another user.
func ownerOnlyDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the phone socket folder: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("read the phone socket folder: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("the phone socket folder %s is not a folder of this user", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: a folder needs its search bit; 0700 is owner-only.
		return fmt.Errorf("restrict the phone socket folder: %w", err)
	}
	return nil
}

// removeStaleSocket removes a socket nobody listens on any more. A live one
// is ErrSocketInUse; anything that is not a socket is left alone and refused.
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the phone socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s is not a socket; leaving it", path)
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		_ = c.Close()
		return ErrSocketInUse
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove the stale phone socket: %w", err)
	}
	return nil
}

// peerListener hands over only the connections peerAllowed accepts.
type peerListener struct {
	*net.UnixListener
	expected func() (int, error)
	self     func() int
	log      *slog.Logger
}

// Accept waits for the next allowed connection, closing refused ones.
func (l *peerListener) Accept() (net.Conn, error) {
	for {
		c, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		peer, err := peerUID(c)
		if err != nil {
			l.log.Warn("lado phone: refused a connection whose peer is unknown", "error", err)
			_ = c.Close()
			continue
		}
		expected, err := l.expected()
		if err != nil {
			l.log.Warn("lado phone: refused a connection; tailscaled's uid is unknown", "peer_uid", peer, "error", err)
			_ = c.Close()
			continue
		}
		if !peerAllowed(peer, expected, l.self()) {
			l.log.Warn("lado phone: refused a connection from a uid other than tailscaled's", "peer_uid", peer, "expected_uid", expected)
			_ = c.Close()
			continue
		}
		return c, nil
	}
}

// peerUID reads the connecting process's uid (SO_PEERCRED).
func peerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var (
		cred    *unix.Ucred
		credErr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) //nolint:gosec // G115: a file descriptor fits in int.
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return int(cred.Uid), nil
}
