package controllers

// LADO fork: phone approval's DTOs (lado_phone.go).

import (
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladophone"
)

// LadoPhoneStatusResponse is phone approval's state.
type LadoPhoneStatusResponse struct {
	// Supported is false off Linux, where phone approval cannot be turned on.
	Supported bool `json:"supported"`
	Enabled   bool `json:"enabled"`
	// RPID, Origin, OwnerLogin and ServePort are what turning it on fixed;
	// they stay after it is turned off.
	RPID       string `json:"rpId" description:"WebAuthn relying party id: the machine's MagicDNS name."`
	Origin     string `json:"origin" description:"https://<MagicDNS name>:<serve port>, the phone pages' origin."`
	OwnerLogin string `json:"ownerLogin" description:"The tailnet login that turned phone approval on; only its requests pass."`
	ServePort  int    `json:"servePort"`
	// Listening says whether the phone socket's server runs.
	Listening bool `json:"listening"`
	// EnrollURL is the page a phone opens to make a passkey.
	EnrollURL string `json:"enrollUrl,omitempty"`
	// EnrollWindowExpiresAt is when the open enrollment closes; absent when
	// none is open.
	EnrollWindowExpiresAt *time.Time `json:"enrollWindowExpiresAt,omitempty"`
	// PairingPending says a phone made a passkey that waits for its code.
	PairingPending bool `json:"pairingPending"`
}

// EnableLadoPhoneRequest turns phone approval on. The body is "{}"; the
// desktop app signs it with a phone_approval.on presence proof whose
// subject is "phone-approval".
type EnableLadoPhoneRequest struct{}

// LookupLadoPhonePairingRequest looks up the passkey a phone made by the
// code the phone shows.
type LookupLadoPhonePairingRequest struct {
	Code string `json:"code" description:"The eight digits the phone shows; spaces and dashes do not matter."`
}

// LadoPhonePairingResponse is a passkey a phone made, for the desktop's
// native confirmation.
type LadoPhonePairingResponse struct {
	DeviceHint       string    `json:"deviceHint"`
	Login            string    `json:"login"`
	CreatedAt        time.Time `json:"createdAt"`
	Fingerprint      string    `json:"fingerprint" description:"The first eight hex characters of the credential id's sha256."`
	CredentialSHA256 string    `json:"credentialSha256" description:"The credential id's sha256 in hex, the subject of the enrollment's presence proof."`
}

// EnrollLadoPhonePasskeyRequest enrolls the pending passkey. The desktop app
// signs the body with a phone_passkey.enroll presence proof whose subject is
// credentialSha256.
type EnrollLadoPhonePasskeyRequest struct {
	Code             string `json:"code"`
	CredentialSHA256 string `json:"credentialSha256" pattern:"^[0-9a-f]{64}$"`
}

// LadoPhonePasskeyIDParam names an enrolled phone passkey.
type LadoPhonePasskeyIDParam struct {
	PasskeyID string `path:"passkeyId" description:"LADO phone passkey id."`
}

// LadoPhonePasskeyResponse is one enrolled phone passkey.
type LadoPhonePasskeyResponse struct {
	ID           string     `json:"id"`
	Fingerprint  string     `json:"fingerprint"`
	DeviceHint   string     `json:"deviceHint"`
	Login        string     `json:"login"`
	RPID         string     `json:"rpId"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastUsedAt   *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt    *time.Time `json:"revokedAt,omitempty"`
	Revoked      bool       `json:"revoked"`
	CloneWarning bool       `json:"cloneWarning" description:"The passkey's sign count once went back; it is refused from then on."`
}

// LadoPhonePasskeyListResponse lists the enrolled passkeys, revoked ones
// too, oldest first.
type LadoPhonePasskeyListResponse struct {
	Passkeys []LadoPhonePasskeyResponse `json:"passkeys"`
}

// LadoPhoneRevokeResponse says whether the request revoked the passkey; a
// passkey revoked already answers false.
type LadoPhoneRevokeResponse struct {
	Revoked bool `json:"revoked"`
}

func ladoPhoneStatusResponse(st ladophone.Status) LadoPhoneStatusResponse {
	resp := LadoPhoneStatusResponse{
		Supported: st.Supported, Enabled: st.Enabled, RPID: st.RPID, Origin: st.Origin, OwnerLogin: st.OwnerLogin,
		ServePort: st.ServePort, Listening: st.Listening, EnrollURL: st.EnrollURL, PairingPending: st.PairingPending,
	}
	if !st.EnrollWindowExpiresAt.IsZero() {
		at := st.EnrollWindowExpiresAt.UTC()
		resp.EnrollWindowExpiresAt = &at
	}
	return resp
}

func ladoPhonePairingResponse(p ladophone.Pairing) LadoPhonePairingResponse {
	return LadoPhonePairingResponse{
		DeviceHint: p.DeviceHint, Login: p.Login, CreatedAt: p.CreatedAt.UTC(),
		Fingerprint: p.Fingerprint, CredentialSHA256: p.CredentialSHA256,
	}
}

func ladoPhonePasskeyResponse(p domain.LadoPhonePasskey) LadoPhonePasskeyResponse {
	resp := LadoPhonePasskeyResponse{
		ID: p.ID, Fingerprint: p.Fingerprint, DeviceHint: p.DeviceHint, Login: p.Login, RPID: p.RPID,
		CreatedAt: p.CreatedAt.UTC(), Revoked: p.Revoked(), CloneWarning: p.CloneWarning,
	}
	if !p.LastUsedAt.IsZero() {
		at := p.LastUsedAt.UTC()
		resp.LastUsedAt = &at
	}
	if !p.RevokedAt.IsZero() {
		at := p.RevokedAt.UTC()
		resp.RevokedAt = &at
	}
	return resp
}
