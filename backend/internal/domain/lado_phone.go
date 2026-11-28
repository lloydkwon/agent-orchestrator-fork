package domain

// LADO fork: approving plans and integrations with a passkey on the
// developer's phone, reached over the tailnet. The settings say whether it
// is on and what turning it on fixed; the passkeys are the ones enrolled
// with the desktop app's confirmation.

import (
	"errors"
	"time"
)

// LadoPhoneSettings is phone approval's one settings row. Turning it on
// fixes the WebAuthn relying party id and origin, the tailnet login that
// owns it, the WebAuthn user handle, the tailscale serve port and the Unix
// socket serve forwards to; turning it off keeps them. EnableProofNonce
// names the presence proof that last turned it on. The zero value is phone
// approval never turned on.
type LadoPhoneSettings struct {
	Enabled          bool
	RPID             string
	Origin           string
	OwnerLogin       string
	UserHandle       []byte
	ServePort        int
	SocketPath       string
	EnableProofNonce string
	UpdatedAt        time.Time
}

// LadoPhonePasskey is one enrolled phone passkey. CredentialJSON is the
// WebAuthn credential as the phone approval service encodes it; SignCount
// and CloneWarning follow the passkey's last use (RecordLadoPhonePasskeyUse)
// and are not written back into CredentialJSON. Fingerprint names the
// passkey in audit actors ("human:passkey:<fingerprint>"). A revoked passkey
// keeps its row. EnrollProofNonce names the desktop presence proof that
// enrolled it.
type LadoPhonePasskey struct {
	ID               string
	CredentialID     []byte
	CredentialJSON   string
	RPID             string
	DeviceHint       string
	Login            string
	Fingerprint      string
	SignCount        uint32
	CloneWarning     bool
	CreatedAt        time.Time
	LastUsedAt       time.Time
	RevokedAt        time.Time
	EnrollProofNonce string
}

// Revoked reports whether the passkey was revoked.
func (p LadoPhonePasskey) Revoked() bool {
	return !p.RevokedAt.IsZero()
}

// LadoPhoneApprovalSubject is the fixed subject of the presence proof that
// turns phone approval on (LadoPresencePhoneApprovalOn): there is one phone
// approval setting, so the proof names no other subject. The desktop app
// signs the request body; the subject only records what was approved.
const LadoPhoneApprovalSubject = "phone-approval"

// ErrLadoPhoneProofRefused means a proof that cannot turn phone approval on
// or enroll a passkey: one for another action, a development proof, or one
// made from a passkey.
var ErrLadoPhoneProofRefused = errors.New("phone approval needs the desktop app's presence proof for this action")

// ErrLadoPhoneApprovalOff means phone approval is off, or its relying party
// is not the one the passkey was made for.
var ErrLadoPhoneApprovalOff = errors.New("phone approval is off or its relying party changed")

// ErrLadoPhonePasskeyExists means the credential, or its fingerprint, is
// enrolled already, revoked or not.
var ErrLadoPhonePasskeyExists = errors.New("the phone passkey is enrolled already")

// ErrLadoPhonePasskeyNotFound means no enrolled passkey has the id, or the
// passkey was revoked.
var ErrLadoPhonePasskeyNotFound = errors.New("no such phone passkey, or it was revoked")
