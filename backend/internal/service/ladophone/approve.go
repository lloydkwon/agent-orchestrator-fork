package ladophone

// Approving one plan or integration from the phone, each with its own
// passkey assertion over exactly what it approves:
//
//  1. The detail (approvals.go) gives the binding: a plan's revision, its
//     sha256 and the rules' sha256; an integration's open PRs, each head with
//     its record's sha256 and the codes to confirm.
//  2. begin checks, as finish does, that tailscale serve still points at
//     the socket, then reads the state again and refuses a binding that no
//     longer matches it, one with production database SQL and one the phone may
//     not approve now. The challenge is
//     SHA256("lado-phone-approve-v1\n" + ceremony id + "\n" + binding sha256),
//     the binding sha256 being that of its canonical JSON, and only the
//     session's passkey may answer it, with the user verified. A session
//     keeps one approval ceremony; a new begin drops the old one.
//  3. finish takes the ceremony out of the table whatever follows, checks
//     that it is the session's own, at most approveTTL old, that phone
//     approval is still on, the passkey still active and tailscale serve
//     still points at the socket, then verifies the assertion. It hands the
//     bound values, never values read again, to LADO's own approval with a
//     proof the passkey made (Via "passkey:<fingerprint>"), which LADO checks
//     and records as it does the desktop's. An integration's proof is given
//     once and only for its bound subject, and Allow refuses database SQL
//     before anything is recorded.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/ladoverify"
)

// approveTTL is how long a phone has between an approval's begin and its
// finish. It is also the passkey proof's lifetime.
const approveTTL = 120 * time.Second

// approveChallengeLabel starts every approval challenge, so no other
// ceremony's challenge can be one.
const approveChallengeLabel = "lado-phone-approve-v1"

// AuditApproved is phone approval's own audit event of an approval, beside
// the one LADO's approval records: it names the binding's and the client
// data's sha256 and the ceremony.
const AuditApproved = "phone_approval.approved"

// maxBindingPRs bounds the PRs of an integration binding.
const maxBindingPRs = 32

// Kinds of approval the phone makes.
const (
	kindPlan        = "plan"
	kindIntegration = "integration"
)

var (
	sha256Hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gitSHA      = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	codeWord    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// planBinding is exactly what a plan approval from the phone approves.
type planBinding struct {
	RevisionID   string `json:"revisionId"`
	SHA256       string `json:"sha256"`
	PolicySHA256 string `json:"policySha256"`
}

// integrationBinding is exactly what an integration approval from the phone
// approves: each open PR's head with its record's hash and the codes to
// confirm, in merge order.
type integrationBinding struct {
	SessionID string                 `json:"sessionId"`
	PRs       []integrationPRBinding `json:"prs"`
}

type integrationPRBinding struct {
	URL                string   `json:"url"`
	Head               string   `json:"head"`
	VerificationSHA256 string   `json:"verificationSha256"`
	Confirm            []string `json:"confirm"`
}

// boundApproval is the approval an approval ceremony signs.
type boundApproval struct {
	kind        string
	plan        planBinding
	integration integrationBinding
	sha256      string
}

// canonical is the binding's canonical JSON: {"kind":…,"binding":{…}} with
// the fields in the order the binding types declare them.
func (b boundApproval) canonical() []byte {
	var binding any = b.plan
	if b.kind == kindIntegration {
		binding = b.integration
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(struct {
		Kind    string `json:"kind"`
		Binding any    `json:"binding"`
	}{b.kind, binding}) // the binding types always encode
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func (b boundApproval) withSHA256() boundApproval {
	sum := sha256.Sum256(b.canonical())
	b.sha256 = hex.EncodeToString(sum[:])
	return b
}

// approveChallenge is the challenge of an approval ceremony.
func approveChallenge(ceremonyID, bindingSHA256 string) []byte {
	sum := sha256.Sum256([]byte(approveChallengeLabel + "\n" + ceremonyID + "\n" + bindingSHA256))
	return sum[:]
}

// decodeBinding reads a begin request's binding strictly and checks its
// form; it says nothing yet about whether the binding is current.
func decodeBinding(kind string, raw json.RawMessage) (boundApproval, error) {
	b := boundApproval{kind: kind}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	switch kind {
	case kindPlan:
		if err := dec.Decode(&b.plan); err != nil {
			return boundApproval{}, err
		}
		p := b.plan
		if !idSegment.MatchString(p.RevisionID) || !sha256Hex64.MatchString(p.SHA256) || !sha256Hex64.MatchString(p.PolicySHA256) {
			return boundApproval{}, errors.New("malformed plan binding")
		}
	case kindIntegration:
		if err := dec.Decode(&b.integration); err != nil {
			return boundApproval{}, err
		}
		in := &b.integration
		if !idSegment.MatchString(in.SessionID) || len(in.PRs) == 0 || len(in.PRs) > maxBindingPRs {
			return boundApproval{}, errors.New("malformed integration binding")
		}
		for i := range in.PRs {
			pr := &in.PRs[i]
			if pr.Confirm == nil {
				pr.Confirm = []string{}
			}
			if !validPRURL(pr.URL) || !gitSHA.MatchString(pr.Head) ||
				!sha256Hex64.MatchString(pr.VerificationSHA256) || len(pr.Confirm) > 64 {
				return boundApproval{}, errors.New("malformed integration binding")
			}
			for _, code := range pr.Confirm {
				if !codeWord.MatchString(code) {
					return boundApproval{}, errors.New("malformed confirm code")
				}
			}
		}
	default:
		return boundApproval{}, errors.New("unknown approval kind")
	}
	if dec.More() {
		return boundApproval{}, errors.New("trailing data after the binding")
	}
	return b.withSHA256(), nil
}

// validPRURL reports whether url is a pull or merge request LADO reads
// (domain.LadoPRRepo): GitHub's on github.com, GitLab's over https or http,
// as an internal GitLab serves it. The detail and begin use it alike.
func validPRURL(url string) bool {
	_, err := domain.LadoPRRepo(url)
	return err == nil
}

// integrationSubject is the presence subject of an integration binding:
// the session and the heads in the binding's (merge) order, as
// ladoverify.ApproveIntegration asks for it.
func integrationSubject(b integrationBinding) string {
	heads := make([]string, 0, len(b.PRs))
	for _, pr := range b.PRs {
		heads = append(heads, strings.ToLower(strings.TrimSpace(pr.Head)))
	}
	return b.SessionID + "@" + strings.Join(heads, ",")
}

// oncePresence is the Presence of an integration approval from the phone:
// it gives proof once, for integration.approve on subject, and refuses
// anything else.
func oncePresence(proof domain.LadoPresenceProof, subject string) func(action, subject string) (domain.LadoPresenceProof, error) {
	var mu sync.Mutex
	used := false
	return func(action, asked string) (domain.LadoPresenceProof, error) {
		mu.Lock()
		defer mu.Unlock()
		if used || action != domain.LadoPresenceIntegrationApprove || asked != subject {
			return domain.LadoPresenceProof{}, apierr.Forbidden("LADO_PHONE_PROOF_MISMATCH", "The phone's signature does not cover this approval.")
		}
		used = true
		proof.Action, proof.SubjectID = action, subject
		return proof, nil
	}
}

// refuseDBSQL is the Allow of an integration approval from the phone: a head
// whose record carries production database SQL is approved on the desktop
// only.
func refuseDBSQL(_ context.Context, _ domain.LadoExecution, prs []ladoverify.ApprovePRResult) error {
	for _, pr := range prs {
		if hasDBSQL(pr.Verification) {
			return desktopOnly()
		}
	}
	return nil
}

func desktopOnly() error {
	return apierr.Forbidden("LADO_PHONE_DESKTOP_ONLY", "This integration carries production database SQL. Approve it in the AO desktop app.")
}

type approveBeginRequest struct {
	Kind    string          `json:"kind"`
	Binding json.RawMessage `json:"binding"`
}

type approveBeginResponse struct {
	Result        string `json:"result"`
	Ceremony      string `json:"ceremony"`
	Options       any    `json:"options"`
	BindingSHA256 string `json:"bindingSha256"`
}

type approveFinishResponse struct {
	Result string `json:"result"`
	Kind   string `json:"kind"`
}

// readAPIJSON decodes an /api request's JSON body strictly, answering 400
// when it cannot.
func readAPIJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPhoneBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		badRequest(w)
		return false
	}
	return true
}

func badRequest(w http.ResponseWriter) {
	writePhoneError(w, http.StatusBadRequest, "LADO_PHONE_BAD_REQUEST", "The request is malformed.")
}

func phoneLocked(w http.ResponseWriter) {
	writePhoneError(w, http.StatusTooManyRequests, "LADO_PHONE_LOCKED", "Too many failed passkey checks. Try again in a few minutes.")
}

func serveMoved(w http.ResponseWriter) {
	writePhoneError(w, http.StatusConflict, "LADO_PHONE_SERVE_MOVED", "Tailscale serve no longer points at phone approval.")
}

func passkeyRevoked(w http.ResponseWriter) {
	writePhoneError(w, http.StatusForbidden, "LADO_PHONE_PASSKEY_REVOKED", "This phone's passkey was revoked.")
}

func (s *Service) handleApproveBegin(w http.ResponseWriter, r *http.Request) {
	var req approveBeginRequest
	if !readAPIJSON(w, r, &req) {
		return
	}
	ctx, now, login := r.Context(), s.now(), r.Header.Get(headerLogin)
	sess, ok := s.sessions.use(sessionToken(r), login, now)
	if !ok {
		loginRequired(w)
		return
	}
	// A session keeps one approval ceremony; any begin drops the old one.
	s.ceremonies.dropSession(ceremonyApprove, sess.key)
	if s.guard.locked(now) {
		phoneLocked(w)
		return
	}
	settings, err := s.store.LadoPhoneSettings(ctx)
	if err != nil {
		s.writeServiceError(w, "read settings", err)
		return
	}
	if !settings.Enabled {
		writePhoneError(w, http.StatusConflict, "LADO_PHONE_OFF", "Phone approval is off.")
		return
	}
	pk, active := s.activePasskey(ctx, settings, sess.credentialID)
	if !active {
		s.sessions.dropPasskey(sess.passkeyID)
		passkeyRevoked(w)
		return
	}
	// As finish does, and before a dry run: a page served from elsewhere
	// on this origin gets no ceremony.
	if !s.serveStillOurs(ctx, settings) {
		serveMoved(w)
		return
	}
	bound, err := decodeBinding(req.Kind, req.Binding)
	if err != nil {
		badRequest(w)
		return
	}
	if !s.currentApproval(ctx, w, sess.key, bound) {
		return
	}
	cred, err := storedCredential(pk)
	if err != nil {
		s.writeServiceError(w, "read the passkey", err)
		return
	}
	rp, err := relyingParty(settings)
	if err != nil {
		s.writeServiceError(w, "relying party", err)
		return
	}
	id := randomToken()
	assertion, data, err := rp.BeginLogin(phoneUser{id: settings.UserHandle, creds: []webauthn.Credential{cred}},
		webauthn.WithChallenge(approveChallenge(id, bound.sha256)),
		webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		s.writeServiceError(w, "begin an approval", err)
		return
	}
	s.ceremonies.put(id, ceremony{
		kind: ceremonyApprove, login: login, rpID: settings.RPID, data: *data, started: now,
		session: sess.key, passkeyID: pk.ID, credentialID: pk.CredentialID, approval: bound,
	})
	s.log.Info("lado phone: an approval waits for the phone's signature", "kind", bound.kind, "binding", bound.sha256, "fingerprint", pk.Fingerprint)
	writePhoneJSON(w, approveBeginResponse{Result: resultOK, Ceremony: id, Options: assertion, BindingSHA256: bound.sha256})
}

// currentApproval reads the approval again for the phone session phone and
// answers the refusal when the binding no longer matches it, it carries
// database SQL, or the phone may not approve it now.
func (s *Service) currentApproval(ctx context.Context, w http.ResponseWriter, phone string, bound boundApproval) bool {
	current := boundApproval{kind: bound.kind}
	var approvable, desktop bool
	switch bound.kind {
	case kindPlan:
		f, err := s.planFacts(ctx, domain.LadoPlanRevisionID(bound.plan.RevisionID))
		if err != nil {
			s.writeServiceError(w, "read a plan", err)
			return false
		}
		current.plan, approvable = f.binding, f.approvable
	case kindIntegration:
		f, err := s.integrationFacts(ctx, phone, domain.SessionID(bound.integration.SessionID))
		if err != nil {
			s.writeServiceError(w, "read an integration", err)
			return false
		}
		current.integration, approvable, desktop = f.binding, f.approvable, f.desktopOnly
	}
	switch {
	case !bytes.Equal(current.canonical(), bound.canonical()):
		writePhoneError(w, http.StatusConflict, "LADO_PHONE_STALE", "This changed since you opened it. Read it again.")
	case desktop:
		s.writeServiceError(w, "", desktopOnly())
	case !approvable:
		writePhoneError(w, http.StatusConflict, "LADO_PHONE_NOT_APPROVABLE", "This cannot be approved now.")
	default:
		return true
	}
	return false
}

func (s *Service) handleApproveFinish(w http.ResponseWriter, r *http.Request) {
	var req finishRequest
	if !readAPIJSON(w, r, &req) {
		return
	}
	ctx, now, login := r.Context(), s.now(), r.Header.Get(headerLogin)
	// The ceremony is gone from here on, whatever happens next.
	c, taken := s.ceremonies.take(req.Ceremony, ceremonyApprove, login, now)
	sess, ok := s.sessions.use(sessionToken(r), login, now)
	if !ok {
		loginRequired(w)
		return
	}
	if !taken || c.session != sess.key || now.Sub(c.started) > approveTTL {
		s.failed(ctx, now, "an approval ceremony that is unknown, used, too old or another session's")
		writePhoneError(w, http.StatusConflict, "LADO_PHONE_CEREMONY_STALE", "This approval timed out or was used. Start it again.")
		return
	}
	if s.guard.locked(now) {
		phoneLocked(w)
		return
	}
	settings, err := s.store.LadoPhoneSettings(ctx)
	if err != nil {
		s.writeServiceError(w, "read settings", err)
		return
	}
	if !settings.Enabled || settings.RPID != c.rpID {
		writePhoneError(w, http.StatusConflict, "LADO_PHONE_OFF", "Phone approval is off.")
		return
	}
	if _, active := s.activePasskey(ctx, settings, c.credentialID); !active {
		s.sessions.dropPasskey(c.passkeyID)
		passkeyRevoked(w)
		return
	}
	if !s.serveStillOurs(ctx, settings) {
		serveMoved(w)
		return
	}
	checked, result := s.checkAssertion(ctx, settings, c, req.Credential, c.credentialID, "approval")
	switch result {
	case resultOK:
	case resultFailed:
		writePhoneError(w, http.StatusForbidden, "LADO_PHONE_SIGNATURE_FAILED", "The passkey's signature did not verify.")
		return
	default:
		writePhoneError(w, http.StatusInternalServerError, "LADO_PHONE_FAILED", "LADO could not check the signature.")
		return
	}
	s.approve(ctx, w, now, req.Ceremony, c, checked)
}

// approve hands the bound approval to LADO with the passkey's proof and
// audits the binding once LADO approved it. The proof is used at now, the
// time finish checked the ceremony's age against, so it is never used
// after it expires.
func (s *Service) approve(ctx context.Context, w http.ResponseWriter, now time.Time, ceremonyID string, c ceremony, checked checkedAssertion) {
	pk, bound := checked.passkey, c.approval
	proof := domain.LadoPresenceProof{
		Nonce:     "pk-" + ceremonyID,
		Via:       domain.LadoPresenceViaPasskey + pk.Fingerprint,
		ExpiresAt: c.started.Add(approveTTL).UTC(),
		UsedAt:    now.UTC(),
	}
	var subjectKind, subjectID string
	var err error
	switch bound.kind {
	case kindPlan:
		b := bound.plan
		proof.Action, proof.SubjectID = domain.LadoPresencePlanApprove, b.RevisionID
		subjectKind, subjectID = "plan_revision", b.RevisionID
		if s.tasks == nil {
			err = approvalsUnavailable()
			break
		}
		_, err = s.tasks.ApprovePlan(ctx, domain.LadoPlanRevisionID(b.RevisionID), b.SHA256, b.PolicySHA256, proof)
	case kindIntegration:
		b := bound.integration
		subject := integrationSubject(b)
		proof.Action, proof.SubjectID = domain.LadoPresenceIntegrationApprove, subject
		subjectKind, subjectID = "lado_execution", b.SessionID
		if s.integrations == nil {
			err = approvalsUnavailable()
			break
		}
		prs := make([]ladoverify.ApprovePR, 0, len(b.PRs))
		for _, pr := range b.PRs {
			prs = append(prs, ladoverify.ApprovePR{PRURL: pr.URL, HeadSHA: pr.Head, VerificationSHA256: pr.VerificationSHA256, Acknowledge: pr.Confirm})
		}
		_, err = s.integrations.ApproveIntegration(ctx, ladoverify.ApproveInput{
			SessionID: domain.SessionID(b.SessionID), PRs: prs, Presence: oncePresence(proof, subject), Allow: refuseDBSQL,
		})
	}
	if err != nil {
		s.log.Warn("lado phone: LADO refused an approval from the phone", "kind", bound.kind, "subject", subjectID, "fingerprint", pk.Fingerprint, "error", err)
		s.writeServiceError(w, "approve", err)
		return
	}
	s.audit(ctx, AuditApproved, proof.Actor(), subjectKind, subjectID, map[string]any{
		"kind": bound.kind, "bindingSha256": bound.sha256, "clientDataSha256": checked.clientDataSHA256,
		"ceremonyId": ceremonyID, "proof": proof.Nonce, "passkeyId": pk.ID, "fingerprint": pk.Fingerprint,
	})
	s.log.Info("lado phone: approved from the phone", "kind", bound.kind, "subject", subjectID, "fingerprint", pk.Fingerprint, "binding", bound.sha256)
	writePhoneJSON(w, approveFinishResponse{Result: resultOK, Kind: bound.kind})
}

// serveStillOurs reports whether tailscale serve still points phone
// approval's port at its socket, as turning it on left it.
func (s *Service) serveStillOurs(ctx context.Context, settings domain.LadoPhoneSettings) bool {
	port, socket := settings.ServePort, settings.SocketPath
	if port == 0 {
		port = s.port
	}
	if socket == "" {
		socket = s.socketPath
	}
	target, err := s.ts.ServeTarget(ctx, port)
	if err != nil {
		s.log.Warn("lado phone: read tailscale serve status before an approval", "error", err)
		return false
	}
	if target != ServeTargetFor(socket) {
		s.log.Warn("lado phone: tailscale serve no longer points at the phone socket; approval refused", "port", port, "target", target)
		return false
	}
	return true
}
