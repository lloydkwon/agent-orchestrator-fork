// Portions adapted from mux serve, Copyright (c) 2026 mux contributors, MIT (see LICENSE.mux).

package ladophone

import (
	"log/slog"
	"mime"
	"net/http"
	"net/netip"
	"strings"
)

// Headers tailscale serve sets on a request it forwards. It replaces any the
// client sent with its own values, and drops a forged funnel header
// (measured with tailscale 1.104.7 on a Unix socket target).
const (
	headerLogin          = "Tailscale-User-Login"
	headerFunnel         = "Tailscale-Funnel-Request"
	headerForwardedHost  = "X-Forwarded-Host"
	headerForwardedProto = "X-Forwarded-Proto"
	headerForwardedFor   = "X-Forwarded-For"
)

// securityHeaders go on every response, refusals included. The pages have no
// inline script or style, so the policy can stay 'self'. frame-ancestors
// 'none' keeps another site from framing a page and steering a tap.
var securityHeaders = [][2]string{
	{"Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
		"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"},
	{"X-Content-Type-Options", "nosniff"},
	{"X-Frame-Options", "DENY"},
	{"Referrer-Policy", "no-referrer"},
	{"Cache-Control", "no-store"},
	{"Permissions-Policy", "camera=(), microphone=(), geolocation=()"},
	{"Cross-Origin-Opener-Policy", "same-origin"},
}

// GateConfig is what turning phone approval on fixed about the requests to
// accept.
type GateConfig struct {
	// Host is "<MagicDNS name>:<serve port>", the address the phone opens.
	Host string
	// OwnerLogin is the tailnet login that turned phone approval on.
	OwnerLogin string
	// SelfIPs are this machine's own tailnet addresses: a request from them
	// comes from this machine, not from the phone.
	SelfIPs []netip.Addr
}

// Origin is the phone pages' origin, also the WebAuthn origin.
func (g GateConfig) Origin() string {
	return "https://" + g.Host
}

// Routes are the only requests the phone socket serves, keyed
// "<METHOD> <path>" with the exact path.
type Routes map[string]http.Handler

// Handler is the phone socket's whole HTTP surface: the gate, then routes
// matched exactly. Anything else is 404; no shared API router is mounted.
func Handler(cfg GateConfig, log *slog.Logger, routes Routes) http.Handler {
	return Gate(cfg, log, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, ok := routes[r.Method+" "+r.URL.Path]
		if !ok || r.URL.RawPath != "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.ServeHTTP(w, r)
	}))
}

// Gate refuses every request that did not come from the owner's phone through
// tailscale serve on the fixed address. The response says nothing about why;
// the log does.
func Gate(cfg GateConfig, log *slog.Logger, next http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, h := range securityHeaders {
			w.Header().Set(h[0], h[1])
		}
		if why := cfg.refuse(r); why != "" {
			log.Warn("lado phone: refused a request", "method", r.Method, "path", r.URL.Path, "reason", why)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// refuse returns why the request is refused, or "" to accept it.
func (g GateConfig) refuse(r *http.Request) string {
	// Funnel is never used; a request from the internet is refused even if
	// someone turns funnel on for the port.
	if len(r.Header.Values(headerFunnel)) > 0 {
		return "funnel request"
	}
	// Behind a Unix socket target the Host is "localhost"; the address the
	// phone opened comes in X-Forwarded-Host.
	if host, ok := single(r, headerForwardedHost); !ok || !strings.EqualFold(host, g.Host) {
		return "forwarded host"
	}
	if proto, ok := single(r, headerForwardedProto); !ok || proto != "https" {
		return "forwarded proto"
	}
	if login, ok := single(r, headerLogin); !ok || g.OwnerLogin == "" || !strings.EqualFold(login, g.OwnerLogin) {
		return "tailnet login"
	}
	if !g.fromAnotherNode(r) {
		return "caller address"
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return ""
	}
	// Writes come from the pages' own origin as JSON: another origin cannot
	// send a JSON body without a preflight.
	if origin, ok := single(r, "Origin"); !ok || origin != g.Origin() {
		return "origin"
	}
	if ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || ct != "application/json" {
		return "content type"
	}
	return ""
}

// fromAnotherNode reports whether every address in X-Forwarded-For parses
// and none is this machine's own, so a curl on this machine through the
// tailnet address is refused.
func (g GateConfig) fromAnotherNode(r *http.Request) bool {
	values := r.Header.Values(headerForwardedFor)
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			addr, err := netip.ParseAddr(strings.TrimSpace(part))
			if err != nil {
				return false
			}
			addr = addr.Unmap()
			for _, own := range g.SelfIPs {
				if addr == own.Unmap() {
					return false
				}
			}
		}
	}
	return true
}

// single returns a header that appears exactly once.
func single(r *http.Request, name string) (string, bool) {
	values := r.Header.Values(name)
	if len(values) != 1 || values[0] == "" {
		return "", false
	}
	return values[0], true
}
