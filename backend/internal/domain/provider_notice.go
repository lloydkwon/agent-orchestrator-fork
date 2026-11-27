package domain

import "strings"

// providerInjectedPromptPrefixes 는 Claude Code 가 사람 대신 대화에 넣는 글의 머리다.
// 이 글들도 UserPromptSubmit 훅을 지나 사람 프롬프트처럼 들어온다. 훅 입력에는
// 기록(transcript)의 origin 이 없어서 머리로 가린다.
//
// 근거(2026-11-27, AO DB 의 TUI claude-code 훅 제출과 그 세션들의 transcript):
//   - `<task-notification>`: run_in_background 명령·백그라운드 에이전트가 끝났다는 알림.
//     transcript 의 origin.kind 가 "task-notification" 이다.
//   - `<agent-message from="`, `<cross-session-message from="`: 다른 에이전트·세션이
//     보낸 글. origin.kind 가 "peer" 다.
//   - `[Cross-session delivery notice]`, `[Cross-session idle notice]`: 세션 사이 전달
//     안내. transcript 에서 isMeta 다.
var providerInjectedPromptPrefixes = []string{
	"<task-notification>",
	`<agent-message from="`,
	`<cross-session-message from="`,
	"[Cross-session delivery notice]",
	"[Cross-session idle notice]",
}

// IsProviderInjectedPrompt 는 프롬프트가 사람이 쓴 지시가 아니라 provider 가 넣은
// 알림·전달글인지 본다. 앞머리만 보므로 글 가운데에 같은 태그가 있는 사람 프롬프트
// («what does <task-notification> mean?»)는 사람 프롬프트로 남는다. 사람이 이 태그로
// 글을 시작하면 알림으로 본다. claudeacp 의 claudeTranscriptNotice 와 같은 한계다.
func IsProviderInjectedPrompt(text string) bool {
	text = strings.TrimSpace(text)
	for _, prefix := range providerInjectedPromptPrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}
