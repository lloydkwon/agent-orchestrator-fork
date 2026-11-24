package ladophone

import (
	"embed"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// The socket's HTTP server. Its whole surface is phoneHandler: the gate,
// then the routes below matched exactly.

// Timeouts of the socket's server. Bodies are a few kilobytes of WebAuthn
// data.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 2 * time.Minute
)

//go:embed static
var staticFiles embed.FS

// phoneHandler is everything the phone socket serves under gate.
func (s *Service) phoneHandler(gate GateConfig) http.Handler {
	return Handler(gate, s.log, Routes{
		"GET /enroll":         staticFile("static/enroll.html", "text/html; charset=utf-8"),
		"GET /enroll.js":      staticFile("static/enroll.js", "text/javascript; charset=utf-8"),
		"GET /enroll.css":     staticFile("static/enroll.css", "text/css; charset=utf-8"),
		"POST /enroll/begin":  http.HandlerFunc(s.handleEnrollBegin),
		"POST /enroll/finish": http.HandlerFunc(s.handleEnrollFinish),

		"POST /login/begin":  http.HandlerFunc(s.handleLoginBegin),
		"POST /login/finish": http.HandlerFunc(s.handleLoginFinish),

		"GET /api/approvals":                  http.HandlerFunc(s.handleApprovals),
		"GET /api/approvals/plan/{id}":        http.HandlerFunc(s.handlePlanDetail),
		"GET /api/approvals/integration/{id}": http.HandlerFunc(s.handleIntegrationDetail),
		"POST /api/approve/begin":             http.HandlerFunc(s.handleApproveBegin),
		"POST /api/approve/finish":            http.HandlerFunc(s.handleApproveFinish),
	})
}

func staticFile(name, contentType string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		data, err := staticFiles.ReadFile(name)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(data)
	})
}

// restartLocked opens the socket and serves phoneHandler(gate) on it,
// closing a server already running. The caller holds s.mu.
func (s *Service) restartLocked(gate GateConfig) error {
	s.stopLocked()
	ln, err := Listen(s.socketPath, ListenOptions{ExpectedUID: s.tailscaledUID, Logger: s.log, selfUID: s.selfUID})
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.phoneHandler(gate),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    32 << 10,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Warn("lado phone: the socket's server stopped", "error", err)
		}
	}()
	s.server, s.listener, s.gate = srv, ln, gate
	return nil
}

// stopLocked closes the server and its socket. The listener is closed here
// too, so the socket is gone on return even when Serve has not started yet.
// The caller holds s.mu.
func (s *Service) stopLocked() {
	if s.server == nil {
		return
	}
	if err := s.server.Close(); err != nil {
		s.log.Warn("lado phone: close the socket's server", "error", err)
	}
	_ = s.listener.Close()
	s.server, s.listener, s.gate = nil, nil, GateConfig{}
}
