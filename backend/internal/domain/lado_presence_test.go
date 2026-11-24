package domain

import "testing"

// The audit actor of an approval names how its proof reached LADO: the
// desktop app's proof is "human", a phone passkey's names the passkey, and a
// development proof stays "human:dev-presence" whatever else it carries.
func TestLadoPresenceProofActor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proof LadoPresenceProof
		want  string
	}{
		{"desktop", LadoPresenceProof{Nonce: "n1"}, "human"},
		{"development", LadoPresenceProof{Nonce: "n2", Dev: true}, "human:dev-presence"},
		{"phone passkey", LadoPresenceProof{Nonce: "pk-1", Via: LadoPresenceViaPasskey + "3f9a1c0d2b4e5f60"}, "human:passkey:3f9a1c0d2b4e5f60"},
		{"development wins over a via", LadoPresenceProof{Nonce: "n3", Dev: true, Via: LadoPresenceViaPasskey + "3f9a1c0d2b4e5f60"}, "human:dev-presence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.proof.Actor(); got != tc.want {
				t.Errorf("Actor() = %q, want %q", got, tc.want)
			}
		})
	}
}
