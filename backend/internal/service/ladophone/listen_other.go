//go:build !linux

package ladophone

import "net"

// TailscaledUID is not known off Linux.
func TailscaledUID() (int, error) { return 0, ErrUnsupported }

// Listen is not available off Linux: the socket's only guard is SO_PEERCRED.
func Listen(string, ListenOptions) (net.Listener, error) { return nil, ErrUnsupported }
