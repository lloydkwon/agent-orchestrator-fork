package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const reportDeliveryPrefix = `<ao-report-delivery id="`

// WrapReportDelivery marks an AO-authored report batch so provider prompt
// hooks can correlate semantic acceptance with its durable delivery identity.
func WrapReportDelivery(id, message string) string {
	return fmt.Sprintf("%s%s\">\n%s\n</ao-report-delivery>", reportDeliveryPrefix, id, message)
}

// ReportDeliveryID returns the durable identity from an AO report envelope.
// Delivery ids are generated internally and deliberately restricted so a
// malformed prompt cannot manufacture markup or an unbounded correlation key.
func ReportDeliveryID(message string) (string, bool) {
	return deliveryID(message, reportDeliveryPrefix)
}

func deliveryID(message, prefix string) (string, bool) {
	message = strings.TrimSpace(message)
	if !strings.HasPrefix(message, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(message, prefix)
	end := strings.Index(rest, `">`)
	if end <= 0 || end > 128 {
		return "", false
	}
	id := rest[:end]
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') && r != ':' && r != '-' && r != '_' {
			return "", false
		}
	}
	return id, true
}

const (
	// MaxReportTextCharacters is the hard limit for free-form messages and structured notes.
	MaxReportTextCharacters = 1000
	// ReportBatchFallback is the fixed informational-report fallback deadline.
	ReportBatchFallback = time.Hour
	// ReportSettlementWindow is the fixed window opened by a done report.
	ReportSettlementWindow = 5 * time.Minute
)

// ReportState is an optional worker-originated coordination claim.
type ReportState string

// Supported report states.
const (
	ReportCheckpoint ReportState = "checkpoint"
	ReportNeedsInput ReportState = "needs_input"
	ReportStuck      ReportState = "stuck"
	ReportDone       ReportState = "done"
)

// Valid reports whether s is a supported structured state.
func (s ReportState) Valid() bool {
	switch s {
	case ReportCheckpoint, ReportNeedsInput, ReportStuck, ReportDone:
		return true
	default:
		return false
	}
}

// ReportOutputKind identifies a durable output reference attached to a report.
type ReportOutputKind string

// Supported report output kinds.
const (
	ReportOutputArtifact   ReportOutputKind = "artifact"
	ReportOutputPRCreated  ReportOutputKind = "pr_created"
	ReportOutputPRReviewed ReportOutputKind = "pr_reviewed"
)

// Valid reports whether k is a supported output kind.
func (k ReportOutputKind) Valid() bool {
	switch k {
	case ReportOutputArtifact, ReportOutputPRCreated, ReportOutputPRReviewed:
		return true
	default:
		return false
	}
}

// ReportOutput is an ordered reference to work produced by a worker.
type ReportOutput struct {
	Kind      ReportOutputKind
	Reference string
	Label     string
}

// ReportDeliveryState is the durable outbox lifecycle of a report.
type ReportDeliveryState string

// Report delivery states.
const (
	ReportPending      ReportDeliveryState = "pending"
	ReportClaimed      ReportDeliveryState = "claimed"
	ReportAcknowledged ReportDeliveryState = "acknowledged"
)

// ReportRecord is the durable report and outbox persistence shape. State and
// outputs are independent, and neither changes authoritative session state.
type ReportRecord struct {
	ID        string
	SessionID SessionID
	ProjectID ProjectID
	// SessionDisplayName is resolved at delivery time for presentation. It is
	// intentionally not persisted with the durable report row.
	SessionDisplayName string
	State              ReportState
	Note               string
	Message            string
	Outputs            []ReportOutput
	CreatedAt          time.Time
	DeliveryState      ReportDeliveryState
	AvailableAt        time.Time
	SettlementDeadline time.Time
	RepeatCount        int64
	ClaimToken         string
	ClaimedAt          time.Time
	DeliveryAttempts   int64
	AcknowledgedAt     time.Time
	LastError          string
	DeliveryBatchID    string
}

// ErrInvalidReport reports an invalid report or delivery transition input.
var ErrInvalidReport = errors.New("invalid report")

// ReportInvalidError names the rule an invalid report broke, so callers can
// tell the worker what to fix. errors.Is(err, ErrInvalidReport) holds for it.
type ReportInvalidError struct {
	Reason string
}

func (e *ReportInvalidError) Error() string { return ErrInvalidReport.Error() + ": " + e.Reason }

// Unwrap keeps errors.Is(err, ErrInvalidReport) working for every reason.
func (e *ReportInvalidError) Unwrap() error { return ErrInvalidReport }

func invalidReport(reason string) error { return &ReportInvalidError{Reason: reason} }

// Validate checks content, ownership, output, and delivery-state invariants.
// ProjectID may be empty: a session without a project (ao spawn --standalone)
// keeps its reports too, although no orchestrator receives them.
func (r ReportRecord) Validate() error {
	if r.ID == "" || r.SessionID == "" || r.CreatedAt.IsZero() || r.AvailableAt.IsZero() || r.RepeatCount < 1 {
		return invalidReport("report id, session, creation time, availability, and repeat count are required")
	}
	if err := ValidateReportContent(r.State, r.Note, r.Message, r.Outputs); err != nil {
		return err
	}
	if r.State == ReportDone {
		if r.SettlementDeadline.IsZero() {
			return invalidReport("a done report needs a settlement deadline")
		}
	} else if !r.SettlementDeadline.IsZero() {
		return invalidReport("only a done report has a settlement deadline")
	}
	if err := r.validateDelivery(); err != nil {
		return err
	}
	if r.DeliveryAttempts < 0 {
		return invalidReport("delivery attempts cannot be negative")
	}
	return nil
}

func (r ReportRecord) validateDelivery() error {
	switch r.DeliveryState {
	case ReportPending:
		if r.ClaimToken != "" || !r.ClaimedAt.IsZero() || !r.AcknowledgedAt.IsZero() {
			return invalidReport("a pending report has no claim or acknowledgement")
		}
	case ReportClaimed:
		if r.ClaimToken == "" || r.ClaimedAt.IsZero() || !r.AcknowledgedAt.IsZero() {
			return invalidReport("a claimed report needs a claim token and time and no acknowledgement")
		}
	case ReportAcknowledged:
		if r.ClaimToken == "" || r.ClaimedAt.IsZero() || r.AcknowledgedAt.IsZero() {
			return invalidReport("an acknowledged report needs a claim token, claim time, and acknowledgement time")
		}
	default:
		return invalidReport(fmt.Sprintf("unknown delivery state %q", r.DeliveryState))
	}
	return nil
}

// ValidateReportContent checks the authoritative report payload contract. The
// returned error is a *ReportInvalidError naming the broken rule.
func ValidateReportContent(state ReportState, note, message string, outputs []ReportOutput) error {
	if message != "" {
		switch {
		case state != "" || note != "" || len(outputs) != 0:
			return invalidReport("a message cannot be combined with a state, note, or outputs")
		case strings.TrimSpace(message) == "":
			return invalidReport("the message is blank")
		case utf8.RuneCountInString(message) > MaxReportTextCharacters:
			return invalidReport(fmt.Sprintf("the message is longer than %d characters", MaxReportTextCharacters))
		}
		return nil
	}
	switch {
	case state == "":
		if note != "" {
			return invalidReport("a note needs a state")
		}
		if len(outputs) == 0 {
			return invalidReport("a report needs a message, a state with a note, or outputs")
		}
	case !state.Valid():
		return invalidReport(fmt.Sprintf("unknown state %q", state))
	case strings.TrimSpace(note) == "":
		return invalidReport(fmt.Sprintf("state %q needs a note", state))
	case utf8.RuneCountInString(note) > MaxReportTextCharacters:
		return invalidReport(fmt.Sprintf("the note is longer than %d characters", MaxReportTextCharacters))
	}
	for i, output := range outputs {
		if problem := reportOutputProblem(output); problem != "" {
			return invalidReport(fmt.Sprintf("output %d: %s", i+1, problem))
		}
	}
	return nil
}

// reportOutputProblem returns why output is invalid, or "" when it is valid.
func reportOutputProblem(output ReportOutput) string {
	if !output.Kind.Valid() {
		return fmt.Sprintf("unknown kind %q", output.Kind)
	}
	if strings.TrimSpace(output.Reference) == "" {
		return "the reference is blank"
	}
	if output.Kind == ReportOutputPRCreated || output.Kind == ReportOutputPRReviewed {
		if _, err := ParseChangeRequestURL(output.Reference); err != nil {
			return fmt.Sprintf("%s needs a pull request or merge request URL", output.Kind)
		}
	}
	return ""
}

// WrapSessionDelivery preserves AO session-origin direction as coordination in
// terminal prompt hooks, without labeling it as a human prompt or worker report.
func WrapSessionDelivery(id, message string) string {
	return fmt.Sprintf("<ao-session-delivery id=%q>\n%s\n</ao-session-delivery>", id, message)
}

// CoordinationDeliveryID recognizes both durable reports and session sends.
func CoordinationDeliveryID(message string) (string, bool) {
	if id, ok := ReportDeliveryID(message); ok {
		return id, true
	}
	return deliveryID(message, `<ao-session-delivery id="`)
}
