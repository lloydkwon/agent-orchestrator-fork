package ladophone

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

const (
	testDNS    = "box.tail1234.ts.net"
	testHost   = testDNS + ":8444"
	testOrigin = "https://" + testHost
	testOwner  = "dev@example.com"
	testPeerIP = "100.64.0.7"
	testSelfV4 = "100.101.102.103"
	testSelfV6 = "fd7a:115c:a1e0::1"
)

func testGateConfig() GateConfig {
	return GateConfig{
		Host:       testHost,
		OwnerLogin: testOwner,
		SelfIPs:    []netip.Addr{netip.MustParseAddr(testSelfV4), netip.MustParseAddr(testSelfV6)},
	}
}

// phoneRequest is a request as tailscale serve delivers it from the owner's
// phone: Host rewritten to localhost, the original address and the caller in
// the forwarding headers.
func phoneRequest(method, path string, edit func(*http.Request)) *http.Request {
	var body *strings.Reader
	if method == http.MethodPost {
		body = strings.NewReader(`{}`)
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, "http://localhost"+path, body)
	r.Host = "localhost"
	r.Header.Set("X-Forwarded-Host", testHost)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-For", testPeerIP)
	r.Header.Set("Tailscale-User-Login", testOwner)
	if method != http.MethodGet && method != http.MethodHead {
		r.Header.Set("Origin", testOrigin)
		r.Header.Set("Content-Type", "application/json")
	}
	if edit != nil {
		edit(r)
	}
	return r
}

// gated runs one request through the gate in front of a handler that answers
// 200, and reports whether the handler ran.
func gated(t *testing.T, r *http.Request) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	log, _ := testLogger()
	reached := false
	h := Gate(testGateConfig(), log, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w, reached
}

func wantRefused(t *testing.T, name string, r *http.Request) {
	t.Helper()
	w, reached := gated(t, r)
	if w.Code != http.StatusForbidden || reached {
		t.Errorf("%s: status %d, handler reached %v; want 403 before the handler", name, w.Code, reached)
	}
	if body := w.Body.String(); len(body) > 32 {
		t.Errorf("%s: refusal body %q tells too much", name, body)
	}
	wantSecurityHeaders(t, name, w)
}

func wantPassed(t *testing.T, name string, r *http.Request) {
	t.Helper()
	w, reached := gated(t, r)
	if w.Code != http.StatusOK || !reached {
		t.Errorf("%s: status %d, handler reached %v; want it through", name, w.Code, reached)
	}
	wantSecurityHeaders(t, name, w)
}

func wantSecurityHeaders(t *testing.T, name string, w *httptest.ResponseRecorder) {
	t.Helper()
	h := w.Header()
	csp := h.Get("Content-Security-Policy")
	for _, part := range []string{"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, part) {
			t.Errorf("%s: CSP %q lacks %q", name, csp, part)
		}
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("%s: CSP allows inline code: %q", name, csp)
	}
	want := map[string]string{
		"Referrer-Policy":        "no-referrer",
		"X-Content-Type-Options": "nosniff",
		"Cache-Control":          "no-store",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s: %s = %q, want %q", name, k, got, v)
		}
	}
}

func TestPhoneGatePassesTheOwnersPhone(t *testing.T) {
	wantPassed(t, "GET", phoneRequest(http.MethodGet, "/enroll", nil))
	wantPassed(t, "POST", phoneRequest(http.MethodPost, "/enroll/begin", nil))
	wantPassed(t, "login in another case", phoneRequest(http.MethodGet, "/enroll", func(r *http.Request) {
		r.Header.Set("Tailscale-User-Login", "Dev@Example.COM")
	}))
	wantPassed(t, "JSON with charset", phoneRequest(http.MethodPost, "/enroll/begin", func(r *http.Request) {
		r.Header.Set("Content-Type", "application/json; charset=utf-8")
	}))
}

func TestPhoneGateRefusesFunnel(t *testing.T) {
	wantRefused(t, "funnel GET", phoneRequest(http.MethodGet, "/enroll", func(r *http.Request) {
		r.Header.Set("Tailscale-Funnel-Request", "?1")
	}))
	wantRefused(t, "funnel POST", phoneRequest(http.MethodPost, "/enroll/begin", func(r *http.Request) {
		r.Header.Set("Tailscale-Funnel-Request", "?1")
	}))
}

func TestPhoneGateRefusesHost(t *testing.T) {
	wantRefused(t, "other host", phoneRequest(http.MethodGet, "/enroll", func(r *http.Request) {
		r.Header.Set("X-Forwarded-Host", "evil.example.com")
	}))
	wantRefused(t, "other port", phoneRequest(http.MethodGet, "/enroll", func(r *http.Request) {
		r.Header.Set("X-Forwarded-Host", testDNS+":443")
	}))
	wantRefused(t, "no port", phoneRequest(http.MethodGet, "/enroll", func(r *http.Request) {
		r.Header.Set("X-Forwarded-Host", testDNS)
	}))
	wantRefused(t, "no forwarded host", phoneRequest(http.MethodGet, "/enroll", func(r *http.Request) {
		r.Header.Del("X-Forwarded-Host")
	}))
	wantRefused(t, "two forwarded hosts", phoneRequest(http.MethodGet, "/enroll", func(r *http.Request) {
		r.Header.Add("X-Forwarded-Host", "evil.example.com")
	}))
	wantRefused(t, "plain http", phoneRequest(http.MethodGet, "/enroll", func(r *http.Request) {
		r.Header.Set("X-Forwarded-Proto", "http")
	}))
	wantRefused(t, "no proto", phoneRequest(http.MethodGet, "/enroll", func(r *http.Request) {
		r.Header.Del("X-Forwarded-Proto")
	}))
}

func TestPhoneGateRefusesOrigin(t *testing.T) {
	for name, origin := range map[string]string{
		"missing":        "",
		"other site":     "https://evil.example.com",
		"plain http":     "http://" + testHost,
		"other port":     "https://" + testDNS + ":443",
		"no port":        "https://" + testDNS,
		"trailing slash": testOrigin + "/",
		"null":           "null",
	} {
		wantRefused(t, "origin "+name, phoneRequest(http.MethodPost, "/enroll/begin", func(r *http.Request) {
			if origin == "" {
				r.Header.Del("Origin")
			} else {
				r.Header.Set("Origin", origin)
			}
		}))
	}
	for name, ct := range map[string]string{
		"missing":   "",
		"form":      "application/x-www-form-urlencoded",
		"multipart": "multipart/form-data; boundary=x",
		"text":      "text/plain",
	} {
		wantRefused(t, "content type "+name, phoneRequest(http.MethodPost, "/enroll/begin", func(r *http.Request) {
			if ct == "" {
				r.Header.Del("Content-Type")
			} else {
				r.Header.Set("Content-Type", ct)
			}
		}))
	}
	wantRefused(t, "DELETE without origin", phoneRequest(http.MethodDelete, "/enroll", func(r *http.Request) {
		r.Header.Del("Origin")
	}))
}

func TestPhoneGateRefusesSelfNode(t *testing.T) {
	for name, xff := range map[string]string{
		"own IPv4":          testSelfV4,
		"own IPv6":          testSelfV6,
		"own IPv6 expanded": "fd7a:115c:a1e0:0:0:0:0:1",
		"own in a list":     testPeerIP + ", " + testSelfV4,
		"missing":           "",
		"garbage":           "phone",
	} {
		wantRefused(t, "XFF "+name, phoneRequest(http.MethodGet, "/enroll", func(r *http.Request) {
			if xff == "" {
				r.Header.Del("X-Forwarded-For")
			} else {
				r.Header.Set("X-Forwarded-For", xff)
			}
		}))
	}
}

func TestPhoneGateRefusesLogin(t *testing.T) {
	wantRefused(t, "other login", phoneRequest(http.MethodGet, "/enroll", func(r *http.Request) {
		r.Header.Set("Tailscale-User-Login", "other@example.com")
	}))
	wantRefused(t, "no login", phoneRequest(http.MethodGet, "/enroll", func(r *http.Request) {
		r.Header.Del("Tailscale-User-Login")
	}))
	wantRefused(t, "two logins", phoneRequest(http.MethodGet, "/enroll", func(r *http.Request) {
		r.Header.Add("Tailscale-User-Login", "other@example.com")
	}))
}

func TestPhoneRoutesExactOnly(t *testing.T) {
	log, _ := testLogger()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := Handler(testGateConfig(), log, Routes{
		"GET /enroll":        ok,
		"POST /enroll/begin": ok,
	})
	serve := func(r *http.Request) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if code := serve(phoneRequest(http.MethodGet, "/enroll", nil)); code != http.StatusOK {
		t.Fatalf("GET /enroll = %d", code)
	}
	if code := serve(phoneRequest(http.MethodPost, "/enroll/begin", nil)); code != http.StatusOK {
		t.Fatalf("POST /enroll/begin = %d", code)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/api/v1/lado/phone/status"},
		{http.MethodPost, "/api/v1/lado/plans/rev-1/approve"},
		{http.MethodGet, "/api/v1/lado/tasks"},
		{http.MethodGet, "/enroll/"},
		{http.MethodGet, "/Enroll"},
		{http.MethodGet, "/enroll.html"},
		{http.MethodPost, "/enroll/begin/"},
		{http.MethodPost, "/enroll/./begin"},
		{http.MethodPost, "/enroll//begin"},
		{http.MethodPost, "/x/../enroll/begin"},
		{http.MethodGet, "/enroll/begin"},
		{http.MethodPost, "/enroll"},
	} {
		if code := serve(phoneRequest(c.method, c.path, nil)); code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", c.method, c.path, code)
		}
	}
	// The gate still runs first: a stranger learns nothing about routes.
	if code := serve(phoneRequest(http.MethodGet, "/enroll", func(r *http.Request) {
		r.Header.Set("Tailscale-User-Login", "other@example.com")
	})); code != http.StatusForbidden {
		t.Fatalf("stranger GET /enroll = %d, want 403", code)
	}
}

func TestPhoneRoutesOneIDSegment(t *testing.T) {
	log, _ := testLogger()
	var got string
	h := Handler(testGateConfig(), log, Routes{
		"GET /api/approvals/plan/{id}": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.PathValue("id")
			w.WriteHeader(http.StatusOK)
		}),
	})
	serve := func(path string) int {
		t.Helper()
		got = ""
		u, err := url.Parse(path)
		if err != nil {
			t.Fatalf("parse %q: %v", path, err)
		}
		r := phoneRequest(http.MethodGet, "/", nil)
		r.URL = u
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for _, id := range []string{"rev-2", "lado-rev_9", "sess.1"} {
		if code := serve("/api/approvals/plan/" + id); code != http.StatusOK || got != id {
			t.Errorf("GET .../plan/%s = %d, id %q", id, code, got)
		}
	}
	for _, path := range []string{
		"/api/approvals/plan/", "/api/approvals/plan/..", "/api/approvals/plan/a..b", "/api/approvals/plan/.x",
		"/api/approvals/plan/a/b", "/api/approvals/plan/a%2Fb", "/api/approvals/plan/{id}", "/api/approvals/plan/%7Bid%7D",
		"/api/approvals/plan", "/api/approvals/plan/" + strings.Repeat("a", 256), "/api/approvals/plan/a b",
	} {
		if code := serve(path); code != http.StatusNotFound || got != "" {
			t.Errorf("GET %s = %d (id %q), want 404", path, code, got)
		}
	}
	if w := httptest.NewRecorder(); true {
		h.ServeHTTP(w, phoneRequest(http.MethodPost, "/api/approvals/plan/rev-2", nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("POST to a GET id route = %d", w.Code)
		}
	}
}
