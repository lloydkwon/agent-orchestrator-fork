package domain

import (
	"errors"
	"time"
)

// LadoPresenceProof is a human-presence proof as LADO records it once used
// (docs/LJY-PRD/adr/0002-human-presence-approval-channel.md). Each nonce is
// stored once, so a proof cannot be replayed, also not after a restart.
type LadoPresenceProof struct {
	Nonce string
	// Action and SubjectID say what the proof was used for, such as
	// LadoPresencePlanApprove and a revision id.
	Action    string
	SubjectID string
	// Dev marks an action taken without presence because the daemon runs with
	// AO_LADO_DEV_PRESENCE=1, for the renderer in a browser during
	// development.
	Dev bool
	// Via says how the proof reached LADO when it is not the desktop app's
	// presence proof: "passkey:<fingerprint>" (LadoPresenceViaPasskey) for an
	// assertion of a passkey enrolled on the developer's phone. The desktop
	// app's proofs and development proofs leave it empty.
	Via       string
	ExpiresAt time.Time
	UsedAt    time.Time
}

// LadoPresenceViaPasskey starts the Via of a proof made from a phone
// passkey's assertion; the passkey's fingerprint follows it.
const LadoPresenceViaPasskey = "passkey:"

// Actor is the audit actor of an action the proof allowed: "human" for the
// desktop app's proof, "human:<Via>" for a proof that came another way, such
// as "human:passkey:<fingerprint>", and "human:dev-presence" for a
// development proof, whatever its Via.
func (p LadoPresenceProof) Actor() string {
	switch {
	case p.Dev:
		return "human:dev-presence"
	case p.Via != "":
		return "human:" + p.Via
	}
	return "human"
}

// Actions that need human presence. LadoPresenceRuleChange is a change to
// a protected rule's settings, such as the permission ceiling's mode or the
// protected paths. The setup actions sign an adoption plan's sha256
// (LadoPresenceSetupAdopt), an adoption id (LadoPresenceSetupRestore), and
// the sha256 of a setup change that can run code or widen access
// (LadoPresenceSetupChange, LadoSetupNeedsPresence).
// LadoPresenceIntegrationApprove approves the integration of one PR head,
// its subject "<session>@<head>", and LadoPresenceCheckCommands changes a
// project's check commands, which the daemon runs outside any agent
// sandbox. LadoPresenceClaudeChromeOn turns Claude in Chrome on for a group
// of sessions; its subject names the settings turned on.
// LadoPresenceDBChanges changes one repository's production database change
// settings or secrets, its subject "<project>:<repo>"
// (LadoDBChangeSettingsSubject), and LadoPresenceDBChangeApply applies a PR
// head's SQL files of one phase, or retries them, its subject
// "<session>@<head>:<phase>" (LadoDBChangeApplySubject, #97).
// LadoPresencePhoneApprovalOn turns on approving plans and integrations
// with a passkey on the developer's phone, and LadoPresencePhonePasskeyEnroll
// enrolls one such passkey. Both take the desktop app's proof only, never a
// development proof or one made from a passkey (the store refuses those).
const (
	LadoPresencePlanApprove        = "plan.approve"
	LadoPresenceModeOff            = "project.lado_mode_off"
	LadoPresenceRuleChange         = "rule.protected_change"
	LadoPresenceSetupAdopt         = "setup.adopt"
	LadoPresenceSetupRestore       = "setup.restore"
	LadoPresenceSetupChange        = "setup.change"
	LadoPresenceIntegrationApprove = "integration.approve"
	LadoPresenceCheckCommands      = "project.check_commands"
	LadoPresenceClaudeChromeOn     = "claude_chrome.on"
	LadoPresenceDBChanges          = "project.db_changes"
	LadoPresenceDBChangeApply      = "db_change.apply"
	LadoPresencePhoneApprovalOn    = "phone_approval.on"
	LadoPresencePhonePasskeyEnroll = "phone_passkey.enroll"
)

// ErrLadoPresenceReplayed means the proof's nonce was already used.
var ErrLadoPresenceReplayed = errors.New("the presence proof was already used")

// ErrLadoPlanSuperseded means a newer revision replaced the one being
// approved, or its bytes changed.
var ErrLadoPlanSuperseded = errors.New("the plan revision is no longer the task's latest")
