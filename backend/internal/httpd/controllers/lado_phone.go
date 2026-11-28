package controllers

// LADO fork: phone approval's loopback routes for the desktop app. The phone
// never reaches these: it talks to the tailscale serve socket only
// (service/ladophone), and the LAN listener blocks /api/v1/lado.
// Turning phone approval on and enrolling a passkey need the desktop app's
// presence proof; the rest need none.

import (
	"context"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladophone"
)

// LadoPhoneService is phone approval as the desktop app drives it.
// *ladophone.Service implements it.
type LadoPhoneService interface {
	Status(ctx context.Context) (ladophone.Status, error)
	Enable(ctx context.Context, proof domain.LadoPresenceProof) (ladophone.Status, error)
	Disable(ctx context.Context) (ladophone.Status, error)
	OpenEnrollWindow(ctx context.Context) (ladophone.Status, error)
	LookupPairing(ctx context.Context, code string) (ladophone.Pairing, error)
	ConfirmEnroll(ctx context.Context, code string, proof domain.LadoPresenceProof) (domain.LadoPhonePasskey, error)
	Passkeys(ctx context.Context) ([]domain.LadoPhonePasskey, error)
	RevokePasskey(ctx context.Context, id string) (bool, error)
}

const (
	ladoPhoneStatusPath        = "/api/v1/lado/phone/status"
	ladoPhoneEnablePath        = "/api/v1/lado/phone/enable"
	ladoPhoneDisablePath       = "/api/v1/lado/phone/disable"
	ladoPhoneEnrollWindowPath  = "/api/v1/lado/phone/enroll-window"
	ladoPhonePairingLookupPath = "/api/v1/lado/phone/pairings/lookup"
	ladoPhonePasskeysPath      = "/api/v1/lado/phone/passkeys"             //nolint:gosec // G101: a route path, not a credential.
	ladoPhonePasskeyPath       = "/api/v1/lado/phone/passkeys/{passkeyId}" //nolint:gosec // G101: a route path, not a credential.
	// maxLadoPhoneBody fits a code and a sha256.
	maxLadoPhoneBody = 4 << 10
)

// ladoSHA256Hex is a sha256 as the enrollment proof's subject spells it.
var ladoSHA256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (c *LadoController) registerPhone(r chi.Router) {
	r.Get("/lado/phone/status", c.getPhoneStatus)
	r.Post("/lado/phone/enable", c.enablePhone)
	r.Post("/lado/phone/disable", c.disablePhone)
	r.Post("/lado/phone/enroll-window", c.openPhoneEnrollWindow)
	r.Post("/lado/phone/pairings/lookup", c.lookupPhonePairing)
	r.Get("/lado/phone/passkeys", c.listPhonePasskeys)
	r.Post("/lado/phone/passkeys", c.enrollPhonePasskey)
	r.Delete("/lado/phone/passkeys/{passkeyId}", c.revokePhonePasskey)
}

func (c *LadoController) getPhoneStatus(w http.ResponseWriter, r *http.Request) {
	if c.Phone == nil {
		apispec.NotImplemented(w, r, "GET", ladoPhoneStatusPath)
		return
	}
	st, err := c.Phone.Status(r.Context())
	writeLadoPhoneStatus(w, r, st, err)
}

func (c *LadoController) enablePhone(w http.ResponseWriter, r *http.Request) {
	if c.Phone == nil {
		apispec.NotImplemented(w, r, "POST", ladoPhoneEnablePath)
		return
	}
	body, ok := readLadoPresenceBody(w, r, maxLadoPhoneBody)
	if !ok {
		return
	}
	// One setting, so the proof's subject is fixed; the signed body is "{}".
	proof, err := c.verifyPresence(r, body, domain.LadoPresencePhoneApprovalOn, domain.LadoPhoneApprovalSubject)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	var req EnableLadoPhoneRequest
	if !decodeLadoBytes(w, r, body, &req) {
		return
	}
	st, err := c.Phone.Enable(r.Context(), proof)
	writeLadoPhoneStatus(w, r, st, err)
}

func (c *LadoController) disablePhone(w http.ResponseWriter, r *http.Request) {
	if c.Phone == nil {
		apispec.NotImplemented(w, r, "POST", ladoPhoneDisablePath)
		return
	}
	st, err := c.Phone.Disable(r.Context())
	writeLadoPhoneStatus(w, r, st, err)
}

func (c *LadoController) openPhoneEnrollWindow(w http.ResponseWriter, r *http.Request) {
	if c.Phone == nil {
		apispec.NotImplemented(w, r, "POST", ladoPhoneEnrollWindowPath)
		return
	}
	st, err := c.Phone.OpenEnrollWindow(r.Context())
	writeLadoPhoneStatus(w, r, st, err)
}

func (c *LadoController) lookupPhonePairing(w http.ResponseWriter, r *http.Request) {
	if c.Phone == nil {
		apispec.NotImplemented(w, r, "POST", ladoPhonePairingLookupPath)
		return
	}
	body, ok := readLadoPresenceBody(w, r, maxLadoPhoneBody)
	if !ok {
		return
	}
	var req LookupLadoPhonePairingRequest
	if !decodeLadoBytes(w, r, body, &req) {
		return
	}
	pairing, err := c.Phone.LookupPairing(r.Context(), req.Code)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ladoPhonePairingResponse(pairing))
}

func (c *LadoController) listPhonePasskeys(w http.ResponseWriter, r *http.Request) {
	if c.Phone == nil {
		apispec.NotImplemented(w, r, "GET", ladoPhonePasskeysPath)
		return
	}
	list, err := c.Phone.Passkeys(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	resp := LadoPhonePasskeyListResponse{Passkeys: make([]LadoPhonePasskeyResponse, 0, len(list))}
	for _, p := range list {
		resp.Passkeys = append(resp.Passkeys, ladoPhonePasskeyResponse(p))
	}
	envelope.WriteJSON(w, http.StatusOK, resp)
}

func (c *LadoController) enrollPhonePasskey(w http.ResponseWriter, r *http.Request) {
	if c.Phone == nil {
		apispec.NotImplemented(w, r, "POST", ladoPhonePasskeysPath)
		return
	}
	body, ok := readLadoPresenceBody(w, r, maxLadoPhoneBody)
	if !ok {
		return
	}
	var req EnrollLadoPhonePasskeyRequest
	if !decodeLadoBytes(w, r, body, &req) {
		return
	}
	if !ladoSHA256Hex.MatchString(req.CredentialSHA256) {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "LADO_PHONE_CREDENTIAL_INVALID",
			"Send the credential's sha256 the pairing lookup returned.", nil)
		return
	}
	// The proof's subject is the credential the desktop showed; the service
	// enrolls only the pending passkey with that sha256.
	proof, err := c.verifyPresence(r, body, domain.LadoPresencePhonePasskeyEnroll, req.CredentialSHA256)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	passkey, err := c.Phone.ConfirmEnroll(r.Context(), req.Code, proof)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ladoPhonePasskeyResponse(passkey))
}

func (c *LadoController) revokePhonePasskey(w http.ResponseWriter, r *http.Request) {
	if c.Phone == nil {
		apispec.NotImplemented(w, r, "DELETE", ladoPhonePasskeyPath)
		return
	}
	revoked, err := c.Phone.RevokePasskey(r.Context(), chi.URLParam(r, "passkeyId"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, LadoPhoneRevokeResponse{Revoked: revoked})
}

func writeLadoPhoneStatus(w http.ResponseWriter, r *http.Request, st ladophone.Status, err error) {
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ladoPhoneStatusResponse(st))
}
