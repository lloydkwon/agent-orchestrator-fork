//go:build linux

package ladophone

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ownUID() (int, error) { return os.Getuid(), nil }

// TestPhoneListenerRefusesOwnUIDPeer: a process of the daemon's own user, such
// as an agent, never gets a connection through, even when the expected uid is
// that same user.
func TestPhoneListenerRefusesOwnUIDPeer(t *testing.T) {
	log, logs := testLogger()
	path := filepath.Join(shortTempDir(t), "phone", "phone.sock")
	ln, err := Listen(path, ListenOptions{ExpectedUID: ownUID, Logger: log})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- c
		}
	}()

	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("read on a refused connection = %v, want EOF (closed by the listener)", err)
	}
	select {
	case got := <-accepted:
		_ = got.Close()
		t.Fatal("Accept returned a connection from the daemon's own uid")
	case <-time.After(200 * time.Millisecond):
	}
	if !strings.Contains(logs.String(), "refused") {
		t.Fatalf("refusal not logged: %q", logs.String())
	}
}

// TestPhoneListenerAcceptsExpectedPeer checks the whole accept path with a
// peer of the expected uid. Only root can connect as another uid, so the test
// tells the listener the daemon runs as someone else.
func TestPhoneListenerAcceptsExpectedPeer(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "phone", "phone.sock")
	log, _ := testLogger()
	ln, err := Listen(path, ListenOptions{
		ExpectedUID: ownUID, Logger: log,
		selfUID: func() int { return os.Getuid() + 1 },
	})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- c
		}
	}()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	select {
	case got := <-accepted:
		_ = got.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("Accept did not return the expected peer's connection")
	}
}

func TestPhoneListenerRefusesWhenExpectedUIDUnknown(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "phone", "phone.sock")
	log, _ := testLogger()
	ln, err := Listen(path, ListenOptions{
		ExpectedUID: func() (int, error) { return 0, errors.New("no tailscaled") }, Logger: log,
		selfUID: func() int { return os.Getuid() + 1 },
	})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- c
		}
	}()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("read = %v, want EOF", err)
	}
	select {
	case got := <-accepted:
		_ = got.Close()
		t.Fatal("Accept returned a connection while the expected uid is unknown")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestPhoneListenerOwnerOnlyAndReplacesStaleSocket(t *testing.T) {
	dir := filepath.Join(shortTempDir(t), "phone")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "phone.sock")
	// A socket left by a daemon that died.
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()

	log, _ := testLogger()
	ln, err := Listen(path, ListenOptions{ExpectedUID: ownUID, Logger: log})
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("socket folder mode = %o, want 700", perm)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, want a 0600 socket", info.Mode())
	}
	if _, err := Listen(path, ListenOptions{ExpectedUID: ownUID, Logger: log}); err == nil {
		t.Fatal("a second Listen took over a live socket")
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left after Close: %v", err)
	}
}

func TestPhoneListenerKeepsANonSocketFile(t *testing.T) {
	dir := filepath.Join(shortTempDir(t), "phone")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "phone.sock")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	log, _ := testLogger()
	if ln, err := Listen(path, ListenOptions{ExpectedUID: ownUID, Logger: log}); err == nil {
		_ = ln.Close()
		t.Fatal("Listen replaced a regular file")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
		t.Fatalf("file changed: %q, %v", data, err)
	}
}

func TestPhoneListenerRefusesLongPath(t *testing.T) {
	path := filepath.Join(shortTempDir(t), strings.Repeat("d", 100), "phone.sock")
	log, _ := testLogger()
	if ln, err := Listen(path, ListenOptions{ExpectedUID: ownUID, Logger: log}); err == nil {
		_ = ln.Close()
		t.Fatal("Listen accepted a socket path over 107 bytes")
	}
}
