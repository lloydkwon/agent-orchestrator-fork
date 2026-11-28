package ladophone

import (
	"net/http"
	"testing"
)

// paceDryRuns lets every phone session run a full burst of dry runs again,
// for tests that open and begin integrations many times in one session.
func (h *phoneHarness) paceDryRuns() {
	h.clock.advance(dryRunBurst * dryRunInterval)
}

func TestPhoneDryRunRateLimited(t *testing.T) {
	h := newApprovalHarness(t)
	dryRuns := func() int {
		var n int
		h.lado.read(func(f *fakeLado) { n = len(f.dryRuns) })
		return n
	}
	cookie := h.login()
	detailPath := "/api/approvals/" + kindIntegration + "/" + string(testSession)

	// Opening an integration and approving it right away is the normal
	// flow, so it is not slowed down.
	d := h.detail(cookie, kindIntegration, string(testSession))
	h.beginOK(cookie, d)
	if got := dryRuns(); got != dryRunBurst {
		t.Fatalf("dry runs = %d, want %d", got, dryRunBurst)
	}

	// Past the burst, a session gets one dry run per interval: opening and
	// beginning are refused without running one.
	wantError(t, "detail past the limit", h.send(http.MethodGet, detailPath, nil, cookie), http.StatusTooManyRequests, "LADO_PHONE_SLOW_DOWN")
	wantError(t, "begin past the limit", h.approveBegin(cookie, d.Kind, d.Binding), http.StatusTooManyRequests, "LADO_PHONE_SLOW_DOWN")
	if got := dryRuns(); got != dryRunBurst {
		t.Fatalf("refused requests ran dry runs: %d, want %d", got, dryRunBurst)
	}

	// Another session has its own allowance.
	other := h.login()
	h.detail(other, kindIntegration, string(testSession))

	// After the interval the session may run one again, and only one.
	h.clock.advance(dryRunInterval)
	h.detail(cookie, kindIntegration, string(testSession))
	wantError(t, "a second detail in the interval", h.send(http.MethodGet, detailPath, nil, cookie), http.StatusTooManyRequests, "LADO_PHONE_SLOW_DOWN")

	// A plan has no dry run and is never slowed down.
	for range 3 {
		h.detail(cookie, kindPlan, string(testRevision))
	}
}
