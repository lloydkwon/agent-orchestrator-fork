// Portions adapted from mux serve, Copyright (c) 2026 mux contributors, MIT (see LICENSE.mux).

package ladophone

// Enrolling a phone passkey, with the code shown only on the phone:
//
//  1. The desktop app opens a ten-minute enrollment (OpenEnrollWindow; no
//     presence needed, it enrolls nothing by itself).
//  2. The phone begins and finishes a WebAuthn registration on /enroll. The
//     server verifies it and holds the credential in memory only, with an
//     eight-digit code it shows on the phone and nowhere else.
//  3. The developer types the code into the desktop app, which looks the
//     pairing up (LookupPairing; five wrong codes close the enrollment) and
//     shows its device, login, time and fingerprint in a native
//     confirmation.
//  4. The desktop app signs a phone_passkey.enroll proof over the
//     credential's sha256 and ConfirmEnroll stores the passkey with it.
//
// The code and any hash of it are never written to a file, the database or
// a log. A second pairing while one waits discards both and closes the
// enrollment.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

const (
	// enrollWindow is how long an enrollment stays open.
	enrollWindow = 10 * time.Minute
	// enrollCodeDigits is the code's length, typed on the desktop.
	enrollCodeDigits = 8
	// enrollMaxMisses wrong codes close the enrollment.
	enrollMaxMisses = 5
	// fingerprintHex is how many hex characters of the credential id's
	// sha256 name a passkey.
	fingerprintHex = 8
	// maxPhoneBody bounds a phone request: WebAuthn responses are a few KB.
	maxPhoneBody = 64 << 10
	// rpDisplayName is the name the phone's passkey sheet shows.
	rpDisplayName = "LADO"
)

// Results of the phone's enrollment requests. The page words them.
const (
	resultOK       = "ok"
	resultClosed   = "closed"   // no enrollment is open, or phone approval is off
	resultStale    = "stale"    // no such ceremony, or it is too old: start again
	resultFailed   = "failed"   // the response did not verify
	resultConflict = "conflict" // a second pairing: both discarded, enrollment closed
	resultBroken   = "broken"   // the server could not read or build what it needs
)

// Pairing is a passkey a phone made, as the desktop app shows it before the
// developer confirms it.
type Pairing struct {
	DeviceHint string
	Login      string
	CreatedAt  time.Time
	// Fingerprint names the passkey: the first eight hex characters of its
	// credential id's sha256.
	Fingerprint string
	// CredentialSHA256 is the credential id's full sha256 in hex, the subject
	// of the enrollment's presence proof.
	CredentialSHA256 string
}

// pendingPasskey is a verified credential waiting for the desktop's
// confirmation. It lives in memory only.
type pendingPasskey struct {
	credential webauthn.Credential
	rpID       string
	pairing    Pairing
	code       string
}

// enrollment is the open enrollment and its one pending passkey.
type enrollment struct {
	ceremonies *ceremonyTable

	mu      sync.Mutex
	until   time.Time
	misses  int
	pending *pendingPasskey
}

func (e *enrollment) open(now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closeLocked()
	e.until = now.Add(enrollWindow)
}

func (e *enrollment) close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closeLocked()
}

func (e *enrollment) closeLocked() {
	e.until, e.misses, e.pending = time.Time{}, 0, nil
	e.ceremonies.drop(ceremonyEnroll)
}

func (e *enrollment) openLocked(now time.Time) bool {
	return !e.until.IsZero() && now.Before(e.until)
}

func (e *enrollment) isOpen(now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.openLocked(now)
}

// state reports when the open enrollment closes (zero when none is open)
// and whether a pairing waits.
func (e *enrollment) state(now time.Time) (time.Time, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.openLocked(now) {
		return time.Time{}, false
	}
	return e.until, e.pending != nil
}

// hold keeps p as the pending passkey. A second one discards both and
// closes the enrollment.
func (e *enrollment) hold(p *pendingPasskey, now time.Time) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.openLocked(now) {
		return resultClosed
	}
	if e.pending != nil {
		e.closeLocked()
		return resultConflict
	}
	e.pending = p
	return resultOK
}

// OpenEnrollWindow opens a ten-minute enrollment, discarding one already
// open with its pairing. Phone approval must be on and its socket open.
func (s *Service) OpenEnrollWindow(ctx context.Context) (Status, error) {
	settings, err := s.store.LadoPhoneSettings(ctx)
	if err != nil {
		s.log.Error("lado phone: read settings", "error", err)
		return Status{}, apierr.Internal("LADO_PHONE_READ_FAILED", "Failed to read the phone approval settings")
	}
	s.mu.Lock()
	listening := s.server != nil
	s.mu.Unlock()
	if !settings.Enabled {
		return Status{}, phoneOff()
	}
	if !listening {
		return Status{}, apierr.Conflict("LADO_PHONE_NOT_LISTENING", "Phone approval is on but its socket is closed. Turn it on again.", nil)
	}
	s.enroll.open(s.now())
	s.log.Info("lado phone: enrollment open", "until", s.now().Add(enrollWindow).UTC().Format(time.RFC3339))
	return s.status(settings), nil
}

// LookupPairing returns the pairing whose code the developer typed. A wrong
// code counts; the fifth closes the enrollment and discards the pairing.
// Spaces and dashes in code do not matter.
func (s *Service) LookupPairing(_ context.Context, code string) (Pairing, error) {
	e := s.enroll
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.openLocked(s.now()) {
		return Pairing{}, enrollClosed()
	}
	if e.pending == nil {
		return Pairing{}, apierr.NotFound("LADO_PHONE_PAIRING_NOT_FOUND", "No phone waits with a code. Make the passkey on the phone first.")
	}
	if !codeMatches(e.pending.code, code) {
		e.misses++
		if e.misses >= enrollMaxMisses {
			e.closeLocked()
			s.log.Warn("lado phone: too many wrong pairing codes; enrollment closed")
			return Pairing{}, apierr.Forbidden("LADO_PHONE_PAIRING_LOCKED", "Too many wrong codes. The enrollment was closed; start again.")
		}
		remaining := enrollMaxMisses - e.misses
		s.log.Warn("lado phone: wrong pairing code", "remaining", remaining)
		return Pairing{}, apierr.Invalid("LADO_PHONE_PAIRING_CODE_WRONG", "That code does not match the phone.", map[string]any{"remaining": remaining})
	}
	return e.pending.pairing, nil
}

// ConfirmEnroll enrolls the pending passkey with the desktop app's proof of
// domain.LadoPresencePhonePasskeyEnroll whose subject is the credential's
// sha256 (Pairing.CredentialSHA256). It takes the pairing and closes the
// enrollment whatever the outcome, so a refused or failed confirmation
// starts over on the phone.
func (s *Service) ConfirmEnroll(ctx context.Context, code string, proof domain.LadoPresenceProof) (domain.LadoPhonePasskey, error) {
	now := s.now()
	e := s.enroll
	e.mu.Lock()
	open, p := e.openLocked(now), e.pending
	e.closeLocked()
	e.mu.Unlock()
	switch {
	case !open:
		return domain.LadoPhonePasskey{}, enrollClosed()
	case p == nil:
		return domain.LadoPhonePasskey{}, apierr.NotFound("LADO_PHONE_PAIRING_NOT_FOUND", "No phone waits with a code. Make the passkey on the phone first.")
	case !desktopProof(proof, domain.LadoPresencePhonePasskeyEnroll) || proof.SubjectID != p.pairing.CredentialSHA256:
		s.log.Warn("lado phone: refused an enrollment proof", "fingerprint", p.pairing.Fingerprint,
			"dev", proof.Dev, "via", proof.Via, "action", proof.Action)
		return domain.LadoPhonePasskey{}, proofRefused()
	case !codeMatches(p.code, code):
		s.log.Warn("lado phone: wrong pairing code at confirmation; pairing discarded", "fingerprint", p.pairing.Fingerprint)
		return domain.LadoPhonePasskey{}, apierr.Invalid("LADO_PHONE_PAIRING_CODE_WRONG", "That code does not match the phone.", nil)
	}
	credentialJSON, err := json.Marshal(p.credential)
	if err != nil {
		return domain.LadoPhonePasskey{}, apierr.Internal("LADO_PHONE_SAVE_FAILED", "Failed to encode the passkey")
	}
	passkey := domain.LadoPhonePasskey{
		ID:             "lpk_" + randomHex(12),
		CredentialID:   p.credential.ID,
		CredentialJSON: string(credentialJSON),
		RPID:           p.rpID,
		DeviceHint:     p.pairing.DeviceHint,
		Login:          p.pairing.Login,
		Fingerprint:    p.pairing.Fingerprint,
		SignCount:      p.credential.Authenticator.SignCount,
		CreatedAt:      now.UTC(),
	}
	event := s.event(AuditPasskeyEnroll, "phone_passkey", passkey.ID, map[string]any{
		"fingerprint": passkey.Fingerprint, "credentialSha256": p.pairing.CredentialSHA256,
		"deviceHint": passkey.DeviceHint, "login": passkey.Login, "rpId": passkey.RPID,
		"pairedAt": p.pairing.CreatedAt.UTC().Format(time.RFC3339Nano), "proof": proof.Nonce,
	})
	if err := s.store.InsertLadoPhonePasskey(ctx, passkey, proof, event); err != nil {
		s.log.Warn("lado phone: enrolling a passkey failed", "fingerprint", passkey.Fingerprint, "error", err)
		return domain.LadoPhonePasskey{}, s.storeError(err)
	}
	passkey.EnrollProofNonce = proof.Nonce
	s.log.Info("lado phone: enrolled a passkey", "id", passkey.ID, "fingerprint", passkey.Fingerprint, "device", passkey.DeviceHint)
	return passkey, nil
}

// beginResponse answers /enroll/begin.
type beginResponse struct {
	Result   string `json:"result"`
	Ceremony string `json:"ceremony,omitempty"`
	Options  any    `json:"options,omitempty"`
}

type finishRequest struct {
	Ceremony   string          `json:"ceremony"`
	Credential json.RawMessage `json:"credential"`
}

// finishResponse answers /enroll/finish. Code is for the phone's screen
// only.
type finishResponse struct {
	Result      string `json:"result"`
	Code        string `json:"code,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

func (s *Service) handleEnrollBegin(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !readPhoneJSON(w, r, &req) {
		return
	}
	now := s.now()
	if !s.enroll.isOpen(now) {
		writePhoneJSON(w, beginResponse{Result: resultClosed})
		return
	}
	settings, err := s.store.LadoPhoneSettings(r.Context())
	if err != nil {
		s.log.Error("lado phone: read settings", "error", err)
		writePhoneJSON(w, beginResponse{Result: resultBroken})
		return
	}
	if !settings.Enabled {
		writePhoneJSON(w, beginResponse{Result: resultClosed})
		return
	}
	passkeys, err := s.store.LadoPhonePasskeys(r.Context())
	if err != nil {
		s.log.Error("lado phone: read passkeys", "error", err)
		writePhoneJSON(w, beginResponse{Result: resultBroken})
		return
	}
	// Every credential enrolled for this relying party, revoked ones too:
	// the store refuses them again anyway.
	exclude := make([]protocol.CredentialDescriptor, 0, len(passkeys))
	for _, p := range passkeys {
		if p.RPID == settings.RPID {
			exclude = append(exclude, protocol.CredentialDescriptor{Type: protocol.PublicKeyCredentialType, CredentialID: p.CredentialID})
		}
	}
	rp, err := relyingParty(settings)
	if err != nil {
		s.log.Error("lado phone: relying party", "error", err)
		writePhoneJSON(w, beginResponse{Result: resultBroken})
		return
	}
	creation, data, err := rp.BeginRegistration(phoneUser{id: settings.UserHandle}, webauthn.WithExclusions(exclude))
	if err != nil {
		s.log.Error("lado phone: begin registration", "error", err)
		writePhoneJSON(w, beginResponse{Result: resultBroken})
		return
	}
	id := s.ceremonies.begin(ceremony{
		kind: ceremonyEnroll, login: r.Header.Get(headerLogin), rpID: settings.RPID, data: *data, started: now,
	})
	writePhoneJSON(w, beginResponse{Result: resultOK, Ceremony: id, Options: creation})
}

func (s *Service) handleEnrollFinish(w http.ResponseWriter, r *http.Request) {
	var req finishRequest
	if !readPhoneJSON(w, r, &req) {
		return
	}
	now := s.now()
	login := r.Header.Get(headerLogin)
	c, ok := s.ceremonies.take(req.Ceremony, ceremonyEnroll, login, now)
	if !ok {
		writePhoneJSON(w, finishResponse{Result: resultStale})
		return
	}
	if !s.enroll.isOpen(now) {
		writePhoneJSON(w, finishResponse{Result: resultClosed})
		return
	}
	settings, err := s.store.LadoPhoneSettings(r.Context())
	if err != nil {
		s.log.Error("lado phone: read settings", "error", err)
		writePhoneJSON(w, finishResponse{Result: resultBroken})
		return
	}
	if !settings.Enabled || settings.RPID != c.rpID {
		writePhoneJSON(w, finishResponse{Result: resultStale})
		return
	}
	credential, err := verifyRegistration(settings, c, req.Credential)
	if err != nil {
		s.log.Warn("lado phone: a passkey registration did not verify", "login", login, "error", err)
		writePhoneJSON(w, finishResponse{Result: resultFailed})
		return
	}
	sum := sha256.Sum256(credential.ID)
	full := hex.EncodeToString(sum[:])
	code, err := newEnrollCode()
	if err != nil {
		s.log.Error("lado phone: make a pairing code", "error", err)
		writePhoneJSON(w, finishResponse{Result: resultBroken})
		return
	}
	p := &pendingPasskey{
		credential: *credential,
		rpID:       settings.RPID,
		code:       code,
		pairing: Pairing{
			DeviceHint:       deviceHint(r.UserAgent()),
			Login:            login,
			CreatedAt:        now.UTC(),
			Fingerprint:      full[:fingerprintHex],
			CredentialSHA256: full,
		},
	}
	switch result := s.enroll.hold(p, now); result {
	case resultOK:
		s.log.Info("lado phone: a phone made a passkey; it waits for its code in the desktop app",
			"fingerprint", p.pairing.Fingerprint, "device", p.pairing.DeviceHint, "login", login)
		writePhoneJSON(w, finishResponse{Result: resultOK, Code: formatCode(code), Fingerprint: p.pairing.Fingerprint})
	case resultConflict:
		s.log.Warn("lado phone: a second phone pairing; both discarded and enrollment closed", "fingerprint", p.pairing.Fingerprint)
		writePhoneJSON(w, finishResponse{Result: resultConflict})
	default:
		writePhoneJSON(w, finishResponse{Result: result})
	}
}

// verifyRegistration checks a registration response against its ceremony:
// challenge, origin, relying party, user verification and the user handle.
func verifyRegistration(settings domain.LadoPhoneSettings, c ceremony, raw json.RawMessage) (*webauthn.Credential, error) {
	parsed, err := protocol.ParseCredentialCreationResponseBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("read the response: %w", err)
	}
	rp, err := relyingParty(settings)
	if err != nil {
		return nil, err
	}
	credential, err := rp.CreateCredential(phoneUser{id: settings.UserHandle}, c.data, parsed)
	if err != nil {
		return nil, err
	}
	if !credential.Flags.UserVerified {
		return nil, errors.New("the authenticator did not verify the user")
	}
	return credential, nil
}

// relyingParty is WebAuthn for the relying party turning phone approval on
// fixed: the MagicDNS name and the serve port's origin, never a request's
// Host. Every passkey needs the user verified (Face ID, a fingerprint, a
// PIN); attestation is not asked for.
func relyingParty(settings domain.LadoPhoneSettings) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPID:                  settings.RPID,
		RPDisplayName:         rpDisplayName,
		RPOrigins:             []string{settings.Origin},
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationRequired,
		},
	})
}

// phoneUser is phone approval's one WebAuthn user.
type phoneUser struct {
	id    []byte
	creds []webauthn.Credential
}

func (u phoneUser) WebAuthnID() []byte                         { return u.id }
func (u phoneUser) WebAuthnName() string                       { return "lado" }
func (u phoneUser) WebAuthnDisplayName() string                { return "LADO" }
func (u phoneUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// newEnrollCode is eight random digits.
func newEnrollCode() (string, error) {
	limit := new(big.Int).Exp(big.NewInt(10), big.NewInt(enrollCodeDigits), nil)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", enrollCodeDigits, n), nil
}

// formatCode shows the code in two groups of four (1234-5678).
func formatCode(code string) string {
	if len(code) != enrollCodeDigits {
		return code
	}
	return code[:4] + "-" + code[4:]
}

// codeMatches compares the digits of input with want in constant time.
func codeMatches(want, input string) bool {
	var b strings.Builder
	for _, r := range input {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	got := b.String()
	return len(want) == enrollCodeDigits && len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// deviceHint names the phone from its User-Agent with fixed words only, so
// nothing the phone sends reaches the desktop's confirmation as text.
func deviceHint(ua string) string {
	device := ""
	for _, d := range [][2]string{
		{"iPhone", "iPhone"}, {"iPad", "iPad"}, {"Android", "Android"}, {"Macintosh", "Mac"},
		{"Windows", "Windows"}, {"CrOS", "ChromeOS"}, {"Linux", "Linux"},
	} {
		if strings.Contains(ua, d[0]) {
			device = d[1]
			break
		}
	}
	browser := ""
	for _, b := range [][2]string{
		{"SamsungBrowser", "Samsung Internet"}, {"EdgiOS", "Edge"}, {"EdgA", "Edge"}, {"Edg/", "Edge"},
		{"FxiOS", "Firefox"}, {"Firefox", "Firefox"}, {"CriOS", "Chrome"}, {"Chrome", "Chrome"}, {"Safari", "Safari"},
	} {
		if strings.Contains(ua, b[0]) {
			browser = b[1]
			break
		}
	}
	switch {
	case device == "" && browser == "":
		return "unknown device"
	case device == "":
		return browser
	case browser == "":
		return device
	}
	return device + " " + browser
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func enrollClosed() error {
	return apierr.Conflict("LADO_PHONE_ENROLL_CLOSED", "No phone enrollment is open. Start one in the desktop app.", nil)
}

// readPhoneJSON decodes a phone request's JSON body strictly, answering 400
// when it cannot.
func readPhoneJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPhoneBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

func writePhoneJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
