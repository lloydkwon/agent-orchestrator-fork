package report

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// validatingStore checks each record the way the sqlite store does and lists
// reports by session.
type validatingStore struct {
	fakeStore
	bySession map[domain.SessionID][]domain.ReportRecord
}

func (v *validatingStore) CreateReport(ctx context.Context, r domain.ReportRecord) (domain.ReportRecord, error) {
	if err := r.Validate(); err != nil {
		return domain.ReportRecord{}, err
	}
	return v.fakeStore.CreateReport(ctx, r)
}

func (v *validatingStore) ListReportsBySession(_ context.Context, id domain.SessionID) ([]domain.ReportRecord, error) {
	return append([]domain.ReportRecord(nil), v.bySession[id]...), nil
}

// rejectingStore fails every insert with the store's validation error.
type rejectingStore struct{ fakeStore }

func (r *rejectingStore) CreateReport(context.Context, domain.ReportRecord) (domain.ReportRecord, error) {
	return domain.ReportRecord{}, &domain.ReportInvalidError{Reason: "only a done report has a settlement deadline"}
}

func wantInvalid(t *testing.T, err error, code, detail string) {
	t.Helper()
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindInvalid || apiErr.Code != code || !strings.Contains(apiErr.Message, detail) {
		t.Fatalf("err = %#v, want a 400 %s naming %q", err, code, detail)
	}
}

// A session without a project stores its reports with an empty project
// (GitHub issue #8), and the stored record passes the store's validation.
func TestCreateStoresAStandaloneSessionReport(t *testing.T) {
	st := &validatingStore{fakeStore: fakeStore{ok: true, session: domain.SessionRecord{ID: "standalone-1", Kind: domain.KindWorker}}}
	for _, input := range []CreateInput{
		{SessionID: "standalone-1", State: domain.ReportCheckpoint, Note: "halfway"},
		{SessionID: "standalone-1", State: domain.ReportDone, Note: "finished", Outputs: []domain.ReportOutput{{Kind: domain.ReportOutputArtifact, Reference: "evidence.md"}}},
		{SessionID: "standalone-1", State: domain.ReportNeedsInput, Note: "which one?"},
	} {
		r, err := New(Deps{Store: st}).Create(context.Background(), input)
		if err != nil {
			t.Fatalf("create %+v: %v", input, err)
		}
		if r.ProjectID != "" || r.SessionID != "standalone-1" || r.State != input.State {
			t.Fatalf("report = %+v", r)
		}
	}
}

// Invalid content is a 400 INVALID_REPORT naming the rule, whether the service
// or the store finds it, never an internal error.
func TestCreateRejectsInvalidReportsAsBadRequests(t *testing.T) {
	worker := domain.SessionRecord{ID: "ao-2", ProjectID: "ao", Kind: domain.KindWorker}
	_, err := New(Deps{Store: &fakeStore{ok: true, session: worker}}).Create(context.Background(),
		CreateInput{SessionID: "ao-2", State: domain.ReportStuck, Note: "  "})
	wantInvalid(t, err, "INVALID_REPORT", `state "stuck" needs a note`)

	_, err = New(Deps{Store: &rejectingStore{fakeStore{ok: true, session: worker}}}).Create(context.Background(),
		CreateInput{SessionID: "ao-2", State: domain.ReportCheckpoint, Note: "note"})
	wantInvalid(t, err, "INVALID_REPORT", "only a done report has a settlement deadline")
}

func TestListSessionIsOrderedAndNeedsASession(t *testing.T) {
	at := time.Date(2026, 11, 27, 1, 2, 3, 0, time.UTC)
	st := &validatingStore{bySession: map[domain.SessionID][]domain.ReportRecord{
		"standalone-1": {{ID: "b", CreatedAt: at.Add(time.Second)}, {ID: "a", CreatedAt: at}},
	}}
	got, err := New(Deps{Store: st}).ListSession(context.Background(), "standalone-1")
	if err != nil || len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("session reports = %+v, %v", got, err)
	}
	_, err = New(Deps{Store: st}).ListSession(context.Background(), "")
	wantInvalid(t, err, "REPORT_SESSION_REQUIRED", "Session id is required")
}
