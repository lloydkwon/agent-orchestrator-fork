package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// promptAgent records the standing instructions each launch gets.
type promptAgent struct {
	mu       sync.Mutex
	launches []ports.LaunchConfig
}

func (a *promptAgent) Agent(domain.AgentHarness) (ports.Agent, bool) { return a, true }

func (a *promptAgent) GetConfigSpec(context.Context) (ports.ConfigSpec, error) {
	return ports.ConfigSpec{}, nil
}

func (a *promptAgent) GetLaunchCommand(_ context.Context, cfg ports.LaunchConfig) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.launches = append(a.launches, cfg)
	return []string{"launch"}, nil
}

func (a *promptAgent) GetPromptDeliveryStrategy(context.Context, ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	return ports.PromptDeliveryInCommand, nil
}

func (a *promptAgent) GetAgentHooks(context.Context, ports.WorkspaceHookConfig) error { return nil }

func (a *promptAgent) GetRestoreCommand(context.Context, ports.RestoreConfig) ([]string, bool, error) {
	return nil, false, nil
}

func (a *promptAgent) SessionInfo(context.Context, ports.SessionRef) (ports.SessionInfo, bool, error) {
	return ports.SessionInfo{}, false, nil
}

func (a *promptAgent) recorded() []ports.LaunchConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ports.LaunchConfig(nil), a.launches...)
}

type promptRuntime struct{}

func (promptRuntime) Create(context.Context, ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	return ports.RuntimeHandle{ID: "runtime-1"}, nil
}
func (promptRuntime) Destroy(context.Context, ports.RuntimeHandle) error { return nil }
func (promptRuntime) GetOutput(context.Context, ports.RuntimeHandle, int) (string, error) {
	return "", nil
}
func (promptRuntime) IsAlive(context.Context, ports.RuntimeHandle) (bool, error) { return true, nil }
func (promptRuntime) ProbeFencedRuntime(context.Context, ports.FencedRuntimeRef) ports.FencedProbeResult {
	return ports.FencedProbeResult{Liveness: ports.FencedAlive, Reason: ports.FencedReasonExactMatch}
}

type promptWorkspace struct{}

func (promptWorkspace) Create(_ context.Context, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	return ports.WorkspaceInfo{Path: "/ws/" + string(cfg.SessionID), Branch: cfg.Branch, SessionID: cfg.SessionID, ProjectID: cfg.ProjectID}, nil
}
func (promptWorkspace) Destroy(context.Context, ports.WorkspaceInfo) error { return nil }
func (w promptWorkspace) Restore(ctx context.Context, cfg ports.WorkspaceConfig) (ports.WorkspaceInfo, error) {
	return w.Create(ctx, cfg)
}
func (promptWorkspace) ForceDestroy(context.Context, ports.WorkspaceInfo) error { return nil }
func (promptWorkspace) StashUncommitted(context.Context, ports.WorkspaceInfo) (string, error) {
	return "", nil
}
func (promptWorkspace) ApplyPreserved(context.Context, ports.WorkspaceInfo, string) error {
	return nil
}
func (promptWorkspace) AddExclude(context.Context, ports.WorkspaceInfo, ...string) error {
	return nil
}

func committedGitRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mer")
	for _, args := range [][]string{
		{"init", "-b", "main", dir},
		{"-C", dir, "-c", "user.email=ao@example.com", "-c", "user.name=AO Test", "commit", "--allow-empty", "-m", "initial"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// Regression test for #4: the app registers a project with POST
// /api/v1/projects and, once that returns, makes its orchestrator with POST
// /api/v1/sessions. Add writes the project's LADO settings (#3), so the first
// orchestrator's standing instructions already carry LADO's section.
func TestNewProjectsFirstOrchestratorGetsLadoInstructions(t *testing.T) {
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	store := sqlitetest.MustOpen(t)
	log := slog.New(slog.DiscardHandler)
	agents := &promptAgent{}
	sessions := sessionmanager.New(sessionmanager.Deps{
		Runtime: promptRuntime{}, Agents: agents, Workspace: promptWorkspace{}, Store: store,
		Lifecycle: lifecycle.New(store, &captureMessenger{}),
		DataDir:   t.TempDir(), LookPath: func(string) (string, error) { return "/usr/bin/true", nil },
	})
	// As daemon.go wires them: wireLado gives the session manager LADO's
	// section, and its settings service gives new projects their defaults.
	lado := wireLado(store, sessions, &ladoGateSink{}, nil, log)
	projects := projectsvc.NewWithDeps(projectsvc.Deps{Store: store, LadoDefaults: lado})
	api := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Projects: projects, Sessions: sessionsvc.New(sessions, store), LadoProjects: lado,
	}, httpd.ControlDeps{}))
	t.Cleanup(api.Close)
	post := func(path string, body map[string]any) []byte {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := api.Client().Post(api.URL+path, "application/json", strings.NewReader(string(raw)))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
			t.Fatalf("POST %s: status %d, body %s", path, resp.StatusCode, out)
		}
		return out
	}

	post("/api/v1/projects", map[string]any{"path": committedGitRepo(t), "projectId": "mer"})
	post("/api/v1/sessions", map[string]any{"projectId": "mer", "kind": "orchestrator", "harness": "claude-code", "mode": "tui"})

	launches := agents.recorded()
	if len(launches) != 1 {
		t.Fatalf("launches = %d, want the orchestrator launched once", len(launches))
	}
	if prompt := launches[0].SystemPrompt; !strings.Contains(prompt, "## LADO project") {
		t.Fatalf("first orchestrator's instructions lack LADO's section:\n%s", prompt)
	}
}
