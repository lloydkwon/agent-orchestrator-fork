package domain

import "testing"

func TestIsProviderInjectedPrompt(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want bool
	}{
		{"백그라운드 완료 알림", "<task-notification>\n<task-id>b1</task-id>\n<status>completed</status>\n</task-notification>", true},
		{"앞 공백이 있는 알림", "  \n<task-notification><task-id>b1</task-id></task-notification>", true},
		{"서브에이전트 회신", `<agent-message from="a1b2c3">done</agent-message>`, true},
		{"세션 사이 메시지", `<cross-session-message from="peer-1">hello</cross-session-message>`, true},
		{"세션 사이 전달 안내", "[Cross-session delivery notice] message delivered", true},
		{"세션 사이 대기 안내", "[Cross-session idle notice] peer is idle", true},
		{"사람 프롬프트", "사이드바 둘째 줄을 고쳐 줘", false},
		{"태그를 묻는 사람 프롬프트", "what does <task-notification> mean?", false},
		{"AO 전달 감싸개", "<ao-session-delivery id=\"d1\">\nhi\n</ao-session-delivery>", false},
		{"빈 글", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsProviderInjectedPrompt(tc.text); got != tc.want {
				t.Fatalf("IsProviderInjectedPrompt(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
