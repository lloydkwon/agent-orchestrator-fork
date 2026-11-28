// Portions adapted from mux serve, Copyright (c) 2026 mux contributors, MIT (see LICENSE.mux).

package ladophone

// Logging the phone in with its passkey, and checking any passkey assertion
// against its ceremony. A login gives a read-only session; approving takes a
// fresh assertion over the approval (approve.go).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Audit actions of passkey checks that went wrong.
const (
	AuditSignatureFailed = "phone_passkey.signature_failed"
	AuditCloneWarning    = "phone_passkey.clone_warning"
	AuditLoginLocked     = "phone_approval.login_locked"
)

// Results of the login requests beside those of enroll.go. The page words
// them.
const (
	resultNone   = "none"   // no passkey can log in: none is enrolled, or all are revoked
	resultLocked = "locked" // too many failed checks; logins rest a while
)

// loginView answers /login/begin and /login/finish.
type loginView struct {
	Result      string `json:"result"`
	Ceremony    string `json:"ceremony,omitempty"`
	Options     any    `json:"options,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

func (s *Service) handleLoginBegin(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !readPhoneJSON(w, r, &req) {
		return
	}
	ctx, now := r.Context(), s.now()
	if s.guard.locked(now) {
		writePhoneJSON(w, loginView{Result: resultLocked})
		return
	}
	settings, err := s.store.LadoPhoneSettings(ctx)
	if err != nil {
		s.log.Error("lado phone: read settings", "error", err)
		writePhoneJSON(w, loginView{Result: resultBroken})
		return
	}
	if !settings.Enabled {
		writePhoneJSON(w, loginView{Result: resultClosed})
		return
	}
	creds, err := s.loginCredentials(ctx, settings)
	if err != nil {
		writePhoneJSON(w, loginView{Result: resultBroken})
		return
	}
	if len(creds) == 0 {
		writePhoneJSON(w, loginView{Result: resultNone})
		return
	}
	rp, err := relyingParty(settings)
	if err != nil {
		s.log.Error("lado phone: relying party", "error", err)
		writePhoneJSON(w, loginView{Result: resultBroken})
		return
	}
	assertion, data, err := rp.BeginLogin(phoneUser{id: settings.UserHandle, creds: creds}, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		s.log.Error("lado phone: begin login", "error", err)
		writePhoneJSON(w, loginView{Result: resultBroken})
		return
	}
	id := s.ceremonies.begin(ceremony{kind: ceremonyLogin, login: r.Header.Get(headerLogin), rpID: settings.RPID, data: *data, started: now})
	writePhoneJSON(w, loginView{Result: resultOK, Ceremony: id, Options: assertion})
}

// loginCredentials are the passkeys that may log in: enrolled for the
// relying party, not revoked and never suspected of being copied.
func (s *Service) loginCredentials(ctx context.Context, settings domain.LadoPhoneSettings) ([]webauthn.Credential, error) {
	passkeys, err := s.store.LadoPhonePasskeys(ctx)
	if err != nil {
		s.log.Error("lado phone: read passkeys", "error", err)
		return nil, err
	}
	var creds []webauthn.Credential
	for _, p := range passkeys {
		if p.Revoked() || p.CloneWarning || p.RPID != settings.RPID {
			continue
		}
		cred, err := storedCredential(p)
		if err != nil {
			s.log.Error("lado phone: an enrolled passkey is unreadable", "id", p.ID, "error", err)
			continue
		}
		creds = append(creds, cred)
	}
	return creds, nil
}

func (s *Service) handleLoginFinish(w http.ResponseWriter, r *http.Request) {
	var req finishRequest
	if !readPhoneJSON(w, r, &req) {
		return
	}
	ctx, now, login := r.Context(), s.now(), r.Header.Get(headerLogin)
	c, ok := s.ceremonies.take(req.Ceremony, ceremonyLogin, login, now)
	if !ok {
		s.failed(ctx, now, "a login ceremony that is unknown, used or too old")
		writePhoneJSON(w, loginView{Result: resultStale})
		return
	}
	if s.guard.locked(now) {
		writePhoneJSON(w, loginView{Result: resultLocked})
		return
	}
	settings, err := s.store.LadoPhoneSettings(ctx)
	if err != nil {
		s.log.Error("lado phone: read settings", "error", err)
		writePhoneJSON(w, loginView{Result: resultBroken})
		return
	}
	if !settings.Enabled || settings.RPID != c.rpID {
		writePhoneJSON(w, loginView{Result: resultClosed})
		return
	}
	checked, result := s.checkAssertion(ctx, settings, c, req.Credential, nil, "login")
	if result != resultOK {
		writePhoneJSON(w, loginView{Result: result})
		return
	}
	pk := checked.passkey
	token := s.sessions.open(phoneSession{
		passkeyID: pk.ID, fingerprint: pk.Fingerprint, credentialID: pk.CredentialID, login: login,
	}, now)
	setSessionCookie(w, token)
	s.log.Info("lado phone: a phone logged in", "fingerprint", pk.Fingerprint, "device", deviceHint(r.UserAgent()), "login", login)
	writePhoneJSON(w, loginView{Result: resultOK, Fingerprint: pk.Fingerprint})
}

// checkedAssertion is an assertion that verified: the passkey that made it
// and the sha256 of the client data it signed.
type checkedAssertion struct {
	passkey          domain.LadoPhonePasskey
	clientDataSHA256 string
}

// checkAssertion verifies an assertion against its ceremony and records the
// passkey's use. The credential must be an enrolled passkey of the relying
// party, not revoked and never suspected of being copied, and want's when
// want is set; the challenge, origin, relying party, user verification and
// signature must verify. It returns resultOK, resultFailed for an assertion
// that does not pass (counted towards the lockout; an enrolled passkey's is
// audited and raised on the desktop at once) or resultBroken when the
// server could not read what it needs. purpose names the check in the log
// and the audit.
func (s *Service) checkAssertion(ctx context.Context, settings domain.LadoPhoneSettings, c ceremony, raw json.RawMessage, want []byte, purpose string) (checkedAssertion, string) {
	now := s.now()
	parsed, err := protocol.ParseCredentialRequestResponseBytes(raw)
	if err != nil {
		s.failed(ctx, now, purpose+": an unreadable assertion")
		return checkedAssertion{}, resultFailed
	}
	if want != nil && !bytes.Equal(parsed.RawID, want) {
		s.failed(ctx, now, purpose+": an assertion of another credential")
		return checkedAssertion{}, resultFailed
	}
	pk, found, err := s.store.LadoPhonePasskeyByCredentialID(ctx, parsed.RawID)
	if err != nil {
		s.log.Error("lado phone: read a passkey", "error", err)
		return checkedAssertion{}, resultBroken
	}
	switch {
	case !found:
		s.failed(ctx, now, purpose+": a credential that is not enrolled")
		return checkedAssertion{}, resultFailed
	case pk.Revoked() || pk.RPID != settings.RPID:
		s.failed(ctx, now, purpose+": a revoked passkey "+pk.Fingerprint)
		return checkedAssertion{}, resultFailed
	case pk.CloneWarning:
		s.failed(ctx, now, purpose+": passkey "+pk.Fingerprint+" is suspected of being copied")
		return checkedAssertion{}, resultFailed
	}
	creds, err := s.relyingPartyCredentials(ctx, settings, pk)
	if err != nil {
		return checkedAssertion{}, resultBroken
	}
	rp, err := relyingParty(settings)
	if err != nil {
		s.log.Error("lado phone: relying party", "error", err)
		return checkedAssertion{}, resultBroken
	}
	verified, err := rp.ValidateLogin(phoneUser{id: settings.UserHandle, creds: creds}, c.data, parsed)
	if err == nil && !parsed.Response.AuthenticatorData.Flags.HasUserVerified() {
		err = errors.New("the authenticator did not verify the user")
	}
	if err != nil {
		s.signatureFailed(ctx, now, pk, purpose, err)
		return checkedAssertion{}, resultFailed
	}
	if verified.Authenticator.CloneWarning {
		s.cloneWarning(ctx, now, pk, purpose)
		return checkedAssertion{}, resultFailed
	}
	if err := s.store.RecordLadoPhonePasskeyUse(ctx, pk.ID, verified.Authenticator.SignCount, false, now.UTC()); err != nil {
		if errors.Is(err, domain.ErrLadoPhonePasskeyNotFound) {
			s.failed(ctx, now, purpose+": passkey "+pk.Fingerprint+" was revoked meanwhile")
			return checkedAssertion{}, resultFailed
		}
		s.log.Error("lado phone: record a passkey's use", "id", pk.ID, "error", err)
		return checkedAssertion{}, resultBroken
	}
	sum := sha256.Sum256(parsed.Raw.AssertionResponse.ClientDataJSON)
	return checkedAssertion{passkey: pk, clientDataSHA256: hex.EncodeToString(sum[:])}, resultOK
}

// relyingPartyCredentials are the credentials of every passkey enrolled for
// the relying party, asserted first. WebAuthn wants the user to own every
// credential a ceremony allowed, revoked ones too; whether the asserted one
// may sign was checked before.
func (s *Service) relyingPartyCredentials(ctx context.Context, settings domain.LadoPhoneSettings, asserted domain.LadoPhonePasskey) ([]webauthn.Credential, error) {
	cred, err := storedCredential(asserted)
	if err != nil {
		s.log.Error("lado phone: an enrolled passkey is unreadable", "id", asserted.ID, "error", err)
		return nil, err
	}
	creds := []webauthn.Credential{cred}
	passkeys, err := s.store.LadoPhonePasskeys(ctx)
	if err != nil {
		s.log.Error("lado phone: read passkeys", "error", err)
		return nil, err
	}
	for _, p := range passkeys {
		if p.ID == asserted.ID || p.RPID != settings.RPID {
			continue
		}
		if other, err := storedCredential(p); err == nil {
			creds = append(creds, other)
		}
	}
	return creds, nil
}

// storedCredential is an enrolled passkey's WebAuthn credential with its
// sign count and clone warning as the store has them now.
func storedCredential(p domain.LadoPhonePasskey) (webauthn.Credential, error) {
	var cred webauthn.Credential
	if err := json.Unmarshal([]byte(p.CredentialJSON), &cred); err != nil {
		return webauthn.Credential{}, fmt.Errorf("read the credential: %w", err)
	}
	if !bytes.Equal(cred.ID, p.CredentialID) {
		return webauthn.Credential{}, errors.New("the credential's id does not match its row")
	}
	cred.Authenticator.SignCount = p.SignCount
	cred.Authenticator.CloneWarning = p.CloneWarning
	return cred, nil
}

// failed counts a failed check towards the lockout, and locks logins and
// approvals once there were too many.
func (s *Service) failed(ctx context.Context, now time.Time, why string) {
	s.log.Warn("lado phone: a passkey check failed", "reason", why)
	if !s.guard.fail(now) {
		return
	}
	s.log.Warn("lado phone: too many failed passkey checks; logins and approvals rest", "for", loginLockout.String())
	s.audit(ctx, AuditLoginLocked, "daemon", "phone_approval", "phone_approval", map[string]any{
		"failures": loginMaxFailures, "window": loginFailureWindow.String(), "lockout": loginLockout.String(),
	})
	s.alert(ctx, AlertLoginLocked, fmt.Sprintf("Phone approval: %d failed passkey checks within %s; logins rest for %s.",
		loginMaxFailures, loginFailureWindow, loginLockout))
}

// signatureFailed records an enrolled passkey's assertion that did not
// verify: it is audited and raised on the desktop at once.
func (s *Service) signatureFailed(ctx context.Context, now time.Time, pk domain.LadoPhonePasskey, purpose string, err error) {
	s.log.Warn("lado phone: an enrolled passkey's assertion did not verify", "fingerprint", pk.Fingerprint, "purpose", purpose, "error", err)
	s.audit(ctx, AuditSignatureFailed, "daemon", "phone_passkey", pk.ID, map[string]any{
		"fingerprint": pk.Fingerprint, "purpose": purpose, "reason": err.Error(),
	})
	s.alert(ctx, AlertSignatureFailed, fmt.Sprintf("Phone approval: an assertion of passkey %s did not verify (%s).", pk.Fingerprint, purpose))
	s.failed(ctx, now, purpose+": passkey "+pk.Fingerprint+" did not verify")
}

// cloneWarning records that a passkey's sign count went backwards: it is
// marked for good, its sessions end, and it is audited and raised.
func (s *Service) cloneWarning(ctx context.Context, now time.Time, pk domain.LadoPhonePasskey, purpose string) {
	if err := s.store.RecordLadoPhonePasskeyUse(ctx, pk.ID, pk.SignCount, true, now.UTC()); err != nil {
		s.log.Error("lado phone: record a clone warning", "id", pk.ID, "error", err)
	}
	s.sessions.dropPasskey(pk.ID)
	s.log.Warn("lado phone: a passkey's sign count went backwards; it may have been copied and is refused from now on",
		"fingerprint", pk.Fingerprint, "purpose", purpose)
	s.audit(ctx, AuditCloneWarning, "daemon", "phone_passkey", pk.ID, map[string]any{"fingerprint": pk.Fingerprint, "purpose": purpose})
	s.alert(ctx, AlertCloneWarning, fmt.Sprintf("Phone approval: passkey %s may have been copied; it is refused from now on. Revoke it and enroll the phone again.", pk.Fingerprint))
	s.failed(ctx, now, purpose+": passkey "+pk.Fingerprint+" may have been copied")
}

// audit appends one of phone approval's own audit events; a failure is
// logged.
func (s *Service) audit(ctx context.Context, action, actor, kind, subject string, payload map[string]any) {
	ev := s.event(action, kind, subject, payload)
	ev.Actor = actor
	if _, err := s.store.AppendLadoAuditEvent(context.WithoutCancel(ctx), ev); err != nil {
		s.log.Error("lado phone: append an audit event", "action", action, "error", err)
	}
}

// alert raises an alert on the desktop, or only logs it without a notifier.
func (s *Service) alert(ctx context.Context, kind, detail string) {
	if s.notifier == nil {
		s.log.Warn("lado phone: alert", "kind", kind, "detail", detail)
		return
	}
	s.notifier.NotifyPhoneAlert(context.WithoutCancel(ctx), kind, detail)
}
