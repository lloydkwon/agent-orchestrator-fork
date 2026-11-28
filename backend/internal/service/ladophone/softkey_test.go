// Portions adapted from mux serve, Copyright (c) 2026 mux contributors, MIT (see LICENSE.mux).

package ladophone

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
)

// softKey is a test authenticator shaped like an iPhone passkey: ES256, user
// verification, backup flags, a sign count of 0 and no attestation.
// Step 5's login tests use get with the same key.
type softKey struct {
	priv   *ecdsa.PrivateKey
	id     []byte
	user   string
	count  uint32
	origin string
	rpID   string
}

// iPhoneFlags are the flags an iPhone passkey sets: user present and
// verified, backup eligible and backed up.
const iPhoneFlags = protocol.FlagUserPresent | protocol.FlagUserVerified | protocol.FlagBackupEligible | protocol.FlagBackupState

func newSoftKey(t *testing.T) *softKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &softKey{priv: priv, id: id, origin: testOrigin, rpID: testDNS}
}

var b64 = base64.RawURLEncoding.EncodeToString

// beginView is a begin response as the phone page reads it.
type beginView struct {
	Result   string `json:"result"`
	Ceremony string `json:"ceremony"`
	Options  struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			RP        struct {
				ID string `json:"id"`
			} `json:"rp"`
			User struct {
				ID string `json:"id"`
			} `json:"user"`
			AllowCredentials []struct {
				ID string `json:"id"`
			} `json:"allowCredentials"`
			ExcludeCredentials []struct {
				ID string `json:"id"`
			} `json:"excludeCredentials"`
			AuthenticatorSelection struct {
				ResidentKey      string `json:"residentKey"`
				UserVerification string `json:"userVerification"`
			} `json:"authenticatorSelection"`
			Attestation      string `json:"attestation"`
			UserVerification string `json:"userVerification"`
		} `json:"publicKey"`
	} `json:"options"`
}

func (k *softKey) authData(flags protocol.AuthenticatorFlags, attested []byte) []byte {
	hash := sha256.Sum256([]byte(k.rpID))
	d := append([]byte{}, hash[:]...)
	d = append(d, byte(flags))
	d = binary.BigEndian.AppendUint32(d, k.count)
	return append(d, attested...)
}

func (k *softKey) clientData(t *testing.T, typ, challenge string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": k.origin, "crossOrigin": false})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// create answers a registration begin with flags as the authenticator data's
// flags (iPhoneFlags for a passkey that verified the user).
func (k *softKey) create(t *testing.T, b beginView, flags protocol.AuthenticatorFlags) map[string]any {
	t.Helper()
	k.user = b.Options.PublicKey.User.ID
	pub, err := k.priv.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	pt := pub.Bytes() // 0x04 || X || Y
	cose, err := webauthncbor.Marshal(map[int64]any{
		1: int64(webauthncose.EllipticKey), 3: int64(webauthncose.AlgES256), -1: int64(webauthncose.P256),
		-2: pt[1:33], -3: pt[33:65],
	})
	if err != nil {
		t.Fatal(err)
	}
	attested := append([]byte(nil), make([]byte, 16)...) // AAGUID 0
	attested = binary.BigEndian.AppendUint16(attested, uint16(len(k.id)))
	attested = append(append(attested, k.id...), cose...)
	ad := k.authData(flags|protocol.FlagAttestedCredentialData, attested)
	att, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": ad})
	if err != nil {
		t.Fatal(err)
	}
	cd := k.clientData(t, "webauthn.create", b.Options.PublicKey.Challenge)
	return map[string]any{
		"id": b64(k.id), "rawId": b64(k.id), "type": "public-key",
		"response":               map[string]any{"attestationObject": b64(att), "clientDataJSON": b64(cd), "transports": []string{"internal", "hybrid"}},
		"clientExtensionResults": map[string]any{},
	}
}

// get answers a login begin (step 5).
func (k *softKey) get(t *testing.T, b beginView, flags protocol.AuthenticatorFlags) map[string]any {
	t.Helper()
	ad := k.authData(flags, nil)
	cd := k.clientData(t, "webauthn.get", b.Options.PublicKey.Challenge)
	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, k.priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"id": b64(k.id), "rawId": b64(k.id), "type": "public-key",
		"response": map[string]any{
			"authenticatorData": b64(ad), "clientDataJSON": b64(cd), "signature": b64(sig), "userHandle": k.user,
		},
		"clientExtensionResults": map[string]any{},
	}
}

// credentialSHA256 is the hex sha256 of the key's credential id, the subject
// of its enrollment proof.
func (k *softKey) credentialSHA256() string {
	sum := sha256.Sum256(k.id)
	return hex.EncodeToString(sum[:])
}
