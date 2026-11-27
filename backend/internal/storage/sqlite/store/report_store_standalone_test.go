package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// A session without a project (ao spawn --standalone) stores its reports and
// reads them back by session after a restart (GitHub issue #8). They never
// enter the delivery schedule, which serves project orchestrators only, and
// a project's reports keep their delivery as before.
func TestReportStoreKeepsStandaloneSessionReports(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	seedProject(t, s, "ao")
	worker, err := s.CreateSession(ctx, sampleRecord("ao"))
	if err != nil {
		t.Fatal(err)
	}
	standalone, err := s.CreateSession(ctx, sampleRecord(""))
	if err != nil {
		t.Fatal(err)
	}
	if !standalone.IsStandalone() {
		t.Fatalf("session %s has project %q, want none", standalone.ID, standalone.ProjectID)
	}

	now := time.Now().UTC().Truncate(time.Second)
	report := func(id string, session domain.SessionRecord, state domain.ReportState, outputs ...domain.ReportOutput) domain.ReportRecord {
		rec := domain.ReportRecord{
			ID: id, SessionID: session.ID, ProjectID: session.ProjectID, State: state,
			Outputs: outputs, CreatedAt: now, DeliveryState: domain.ReportPending,
			AvailableAt: now, RepeatCount: 1,
		}
		if state != "" {
			rec.Note = "note for " + id
		}
		if state == domain.ReportDone {
			rec.SettlementDeadline = now
		}
		return rec
	}
	standaloneReports := []domain.ReportRecord{
		report("rpt_s1", standalone, domain.ReportCheckpoint),
		report("rpt_s2", standalone, domain.ReportDone,
			domain.ReportOutput{Kind: domain.ReportOutputArtifact, Reference: "evidence.md", Label: "Evidence"},
			domain.ReportOutput{Kind: domain.ReportOutputPRCreated, Reference: "https://github.com/o/r/pull/8"}),
		report("rpt_s3", standalone, domain.ReportNeedsInput),
		report("rpt_s4", standalone, "", domain.ReportOutput{Kind: domain.ReportOutputArtifact, Reference: "only-output.html"}),
	}
	for _, rec := range standaloneReports {
		if _, err := s.CreateReport(ctx, rec); err != nil {
			t.Fatalf("create standalone report %s: %v", rec.ID, err)
		}
	}
	if _, err := s.CreateReport(ctx, report("rpt_p1", worker, domain.ReportCheckpoint)); err != nil {
		t.Fatalf("create project report: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	listed, err := s.ListReportsBySession(ctx, standalone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != len(standaloneReports) {
		t.Fatalf("standalone session lists %d reports, want %d: %+v", len(listed), len(standaloneReports), listed)
	}
	for i, got := range listed {
		want := standaloneReports[i]
		if got.ID != want.ID || got.SessionID != standalone.ID || got.ProjectID != "" || got.State != want.State || got.Note != want.Note || len(got.Outputs) != len(want.Outputs) {
			t.Fatalf("report %d = %+v, want %+v", i, got, want)
		}
	}
	if got := listed[1].Outputs; got[0].Label != "Evidence" || got[1].Kind != domain.ReportOutputPRCreated {
		t.Fatalf("done report outputs = %+v", got)
	}
	if urls, err := s.ListReportedPRURLs(ctx, standalone.ID); err != nil || len(urls) != 1 || urls[0] != "https://github.com/o/r/pull/8" {
		t.Fatalf("standalone reported PR URLs = %v, %v", urls, err)
	}

	byProject, err := s.ListReportsByProject(ctx, "ao")
	if err != nil || len(byProject) != 1 || byProject[0].ID != "rpt_p1" {
		t.Fatalf("project reports = %+v, %v", byProject, err)
	}
	schedule, err := s.ListPendingReportSchedule(ctx, 100)
	if err != nil || len(schedule) != 1 || schedule[0].ID != "rpt_p1" {
		t.Fatalf("delivery schedule = %+v, %v; want only the project report", schedule, err)
	}
	pending, err := s.ListPendingReports(ctx, now.Add(time.Hour), 100)
	if err != nil || len(pending) != 1 || pending[0].ID != "rpt_p1" {
		t.Fatalf("pending reports = %+v, %v; want only the project report", pending, err)
	}
	claimed, err := s.ClaimPendingReportBatch(ctx, "ao", "token", now)
	if err != nil || len(claimed) != 1 || claimed[0].ID != "rpt_p1" {
		t.Fatalf("claimed project batch = %+v, %v", claimed, err)
	}
	if acked, err := s.AcknowledgeReportBatch(ctx, "ao", "token", now); err != nil || acked != 1 {
		t.Fatalf("acknowledged %d, %v; want 1", acked, err)
	}
	for _, rec := range standaloneReports {
		got, ok, err := s.GetReport(ctx, rec.ID)
		if err != nil || !ok || got.DeliveryState != domain.ReportPending {
			t.Fatalf("standalone report %s = %+v, ok %v, err %v; want it pending", rec.ID, got, ok, err)
		}
	}
}
