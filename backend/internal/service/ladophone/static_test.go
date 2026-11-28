package ladophone

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"strings"
	"testing"
)

// staticRoute is where the phone socket serves each embedded file.
var staticRoute = map[string]string{
	"index.html":  "/",
	"app.js":      "/app.js",
	"app.css":     "/app.css",
	"webauthn.js": "/webauthn.js",
	"enroll.html": "/enroll",
	"enroll.js":   "/enroll.js",
	"enroll.css":  "/enroll.css",
}

func TestStaticNoInlineScriptCSP(t *testing.T) {
	entries, err := fs.ReadDir(staticFiles, "static")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != len(staticRoute) {
		t.Fatalf("static files = %v, want exactly %d with routes", names, len(staticRoute))
	}

	scriptTag := regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)
	forbiddenHTML := map[string]*regexp.Regexp{
		"an on* attribute":    regexp.MustCompile(`(?i)<[^>]*\son[a-z]+\s*=`),
		"a style attribute":   regexp.MustCompile(`(?i)<[^>]*\sstyle\s*=`),
		"a style element":     regexp.MustCompile(`(?i)<style\b`),
		"a javascript: URL":   regexp.MustCompile(`(?i)javascript:`),
		"an off-origin asset": regexp.MustCompile(`(?i)(src|href)\s*=\s*"(https?:)?//`),
	}
	forbiddenJS := map[string]*regexp.Regexp{
		"innerHTML":          regexp.MustCompile(`\binnerHTML\b`),
		"outerHTML":          regexp.MustCompile(`\bouterHTML\b`),
		"insertAdjacentHTML": regexp.MustCompile(`\binsertAdjacentHTML\b`),
		"document.write":     regexp.MustCompile(`document\.write`),
		"eval":               regexp.MustCompile(`\beval\s*\(`),
		"new Function":       regexp.MustCompile(`new\s+Function\b`),
		"a string timer":     regexp.MustCompile(`set(Timeout|Interval)\(\s*["'\x60]`),
		"an on* property":    regexp.MustCompile(`\.on[a-z]+\s*=`),
	}
	h := newPhoneHarness(t)
	handler := h.s.phoneHandler(testGateConfig())
	for _, name := range names {
		data, err := staticFiles.ReadFile(path.Join("static", name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		switch path.Ext(name) {
		case ".html":
			tags := scriptTag.FindAllStringSubmatch(text, -1)
			if len(tags) == 0 {
				t.Errorf("%s loads no script", name)
			}
			for _, tag := range tags {
				if !strings.Contains(tag[1], `src="/`) || strings.TrimSpace(tag[2]) != "" {
					t.Errorf("%s has an inline script: %q", name, tag[0])
				}
			}
			for what, re := range forbiddenHTML {
				if re.MatchString(text) {
					t.Errorf("%s has %s", name, what)
				}
			}
			// Each page loads the shared WebAuthn helpers before its own script.
			own := map[string]string{"index.html": `src="/app.js"`, "enroll.html": `src="/enroll.js"`}[name]
			if shared, at := strings.Index(text, `src="/webauthn.js"`), strings.Index(text, own); own == "" || shared < 0 || at < shared {
				t.Errorf("%s does not load /webauthn.js before its own script", name)
			}
		case ".js":
			for what, re := range forbiddenJS {
				if re.MatchString(text) {
					t.Errorf("%s uses %s", name, what)
				}
			}
		}

		route, ok := staticRoute[name]
		if !ok {
			t.Errorf("no route for static/%s", name)
			continue
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, phoneRequest(http.MethodGet, route, nil))
		ct := w.Header().Get("Content-Type")
		wantType := map[string]string{".html": "text/html", ".js": "text/javascript", ".css": "text/css"}[path.Ext(name)]
		if w.Code != http.StatusOK || !strings.HasPrefix(ct, wantType) || w.Body.String() != text {
			t.Errorf("GET %s = %d %q", route, w.Code, ct)
		}
		csp := w.Header().Get("Content-Security-Policy")
		for _, part := range []string{"default-src 'self'", "script-src 'self'", "style-src 'self'", "frame-ancestors 'none'", "form-action 'none'"} {
			if !strings.Contains(csp, part) {
				t.Errorf("GET %s: CSP %q lacks %q", route, csp, part)
			}
		}
		if strings.Contains(csp, "unsafe-") || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("GET %s: headers %v", route, w.Header())
		}
	}
}
