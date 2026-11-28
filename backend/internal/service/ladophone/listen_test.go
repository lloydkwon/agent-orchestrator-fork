package ladophone

import (
	"path/filepath"
	"testing"
)

func TestPhonePeerAllowedOnlyExpectedOtherUID(t *testing.T) {
	cases := []struct {
		name                 string
		peer, expected, self int
		want                 bool
	}{
		{"tailscaled as root", 0, 0, 1000, true},
		{"the user's own process", 1000, 0, 1000, false},
		{"expected is the user too", 1000, 1000, 1000, false},
		{"another user", 1001, 0, 1000, false},
		{"daemon running as root", 0, 0, 0, false},
	}
	for _, tc := range cases {
		if got := peerAllowed(tc.peer, tc.expected, tc.self); got != tc.want {
			t.Errorf("%s: peerAllowed(%d, %d, %d) = %v, want %v", tc.name, tc.peer, tc.expected, tc.self, got, tc.want)
		}
	}
}

func TestPhoneSocketPathUnderLadoState(t *testing.T) {
	got := SocketPath("/home/dev/.ao")
	if want := filepath.Join("/home/dev/.ao", "lado", "phone", "phone.sock"); got != want {
		t.Fatalf("SocketPath = %q, want %q", got, want)
	}
}
