package lifecycle

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// 이슈 #9: Claude Code 가 백그라운드 완료 알림(<task-notification>)이나 다른 세션의 글로
// 턴을 열면 UserPromptSubmit 훅이 그 글을 사람 프롬프트처럼 보낸다. 사이드바 둘째 줄
// (lastUserMessagePreview)은 LatestUserPrompt 를 보이므로, 그 글이 사람의 지시를 밀어내면
// 안 된다. 활동(#6)과 네이티브 증거는 그대로 남아야 한다.
func TestActivity_ProviderInjectedPromptKeepsLatestHumanPrompt(t *testing.T) {
	for name, notice := range map[string]string{
		"task-notification":     "<task-notification>\n<task-id>b1</task-id>\n<tool-use-id>toolu_1</tool-use-id>\n<output-file>/tmp/b1.output</output-file>\n<status>completed</status>\n</task-notification>",
		"cross-session-message": `<cross-session-message from="standalone-2">회신입니다</cross-session-message>`,
	} {
		t.Run(name, func(t *testing.T) {
			m, store, _ := newManager()
			rec := working("mer-1")
			rec.Harness = domain.HarnessClaudeCode
			rec.Mode = domain.SessionModeTUI
			rec.Metadata.RuntimeLaunchID = "launch-1"
			rec.Metadata.AgentSessionID = "native-1"
			rec.Metadata.AgentSessionIDLaunchID = "launch-1"
			store.sessions[rec.ID] = rec

			humanAt := time.Unix(1_000, 0).UTC()
			const human = "사이드바 둘째 줄에 최근 지시를 보여 줘"
			apply := func(signal ports.ActivitySignal) {
				t.Helper()
				signal.Valid, signal.LaunchID, signal.AgentSessionID = true, "launch-1", "native-1"
				if err := m.ApplyActivitySignal(ctx, rec.ID, signal); err != nil {
					t.Fatal(err)
				}
			}
			apply(ports.ActivitySignal{State: domain.ActivityActive, Event: "user-prompt-submit",
				ProviderTurnID: "p1", SubmissionID: "s1", LatestUserPrompt: human, Timestamp: humanAt})
			apply(ports.ActivitySignal{State: domain.ActivityIdle, Event: "stop",
				ProviderTurnID: "p1", LatestAssistantUpdate: "고쳤습니다", Timestamp: humanAt.Add(10 * time.Second)})
			apply(ports.ActivitySignal{State: domain.ActivityActive, Event: "user-prompt-submit",
				ProviderTurnID: "p2", SubmissionID: "s2", LatestUserPrompt: notice, Timestamp: humanAt.Add(time.Minute)})

			got := store.sessions[rec.ID]
			if got.Metadata.LatestUserPrompt != human || !got.Metadata.LatestUserPromptAt.Equal(humanAt) {
				t.Fatalf("알림이 사람 프롬프트를 밀어냈다: %q at %s", got.Metadata.LatestUserPrompt, got.Metadata.LatestUserPromptAt)
			}
			if got.Activity.State != domain.ActivityActive {
				t.Fatalf("알림 턴의 활동 = %q, want active", got.Activity.State)
			}
			var evidence domain.NativeCheckpointEvidence
			if err := json.Unmarshal([]byte(got.Metadata.NativeCheckpointEvidence), &evidence); err != nil {
				t.Fatal(err)
			}
			last := evidence.Events[len(evidence.Events)-1]
			if !last.Submission || last.PromptID != "p2" || last.Text != notice {
				t.Fatalf("알림 턴의 네이티브 증거가 빠졌다: %+v", last)
			}

			apply(ports.ActivitySignal{State: domain.ActivityIdle, Event: "stop",
				ProviderTurnID: "p2", LatestAssistantUpdate: "알림을 확인했습니다", Timestamp: humanAt.Add(70 * time.Second)})
			nextAt := humanAt.Add(2 * time.Minute)
			const next = "이제 시험을 돌려 줘"
			apply(ports.ActivitySignal{State: domain.ActivityActive, Event: "user-prompt-submit",
				ProviderTurnID: "p3", SubmissionID: "s3", LatestUserPrompt: next, Timestamp: nextAt})
			got = store.sessions[rec.ID]
			if got.Metadata.LatestUserPrompt != next || !got.Metadata.LatestUserPromptAt.Equal(nextAt) {
				t.Fatalf("다음 사람 프롬프트 = %q at %s", got.Metadata.LatestUserPrompt, got.Metadata.LatestUserPromptAt)
			}
		})
	}
}
