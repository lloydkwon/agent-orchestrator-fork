/**
 * LADO fork: 스크래치패드 세션 행의 둘째 줄 — 사용자가 그 에이전트에게 가장 최근에 시킨 일.
 *
 * 첫 입력 없이 시작한 스크래치패드 에이전트는 모두 하니스 이름이 제목이라, 둘이면 «claude-code» 가
 * 두 번 보인다. 데몬이 세션마다 최근 사용자 프롬프트를 한 줄(`lastUserMessagePreview`)로 보내고,
 * 여기서는 행이 어느 줄을 보일지만 정한다.
 */

/** 이보다 짧은 프롬프트는 대개 일이 아니라 대답(«응 진행해»)이다. */
const MEANINGFUL_DETAIL_MIN_LENGTH = 10;

const collapseWhitespace = (text: string): string => text.trim().split(/\s+/).join(" ");

/**
 * 기억한 줄의 열쇠. 같은 세션 id 가 호스트 둘에 있을 수 있어 호스트를 붙이고, 데이터 폴더를 새로
 * 만들면(개발 lab 의 `AO_DATA_DIR`) `standalone-1` 이 다시 나오므로 만든 시각도 붙인다. 그러지 않으면
 * 새 세션이 예전 세션의 줄을 물려받는다.
 */
export function sessionDetailKey(session: { id: string; hostId?: string; createdAt?: string }): string {
	return `${session.hostId ?? ""}:${session.id}:${session.createdAt ?? ""}`;
}

/**
 * 전에 보이던 줄과 데몬의 최근 프롬프트로 지금 보일 줄을 정한다.
 * 짧은 대답은 일을 말해 주는 줄을 밀어내지 않고, 데몬이 프롬프트를 더는 갖고 있지 않아도
 * (대화를 비운 경우) 행을 비우지 않는다.
 */
export function resolveSessionDetail(remembered: string | undefined, preview: string | undefined): string | undefined {
	const latest = preview ? collapseWhitespace(preview) : "";
	if (!latest) return remembered;
	// 전개(...)는 코드 포인트로 세어 이모지·한글 한 글자를 1 로 센다.
	if (!remembered || [...latest].length >= MEANINGFUL_DETAIL_MIN_LENGTH) return latest;
	return remembered;
}

/**
 * 제목을 되풀이할 뿐인 줄은 보이지 않는다. 첫 입력과 함께 시작한 스크래치패드 에이전트는
 * 그 입력의 앞 100자가 제목이고, 그것이 첫 프롬프트의 앞부분이기도 하다.
 *
 * 짧은 제목에는 이 규칙을 쓰지 않는다. 하니스 이름(«codex»)이나 사용자가 붙인 짧은 이름(«결제»)으로
 * 시작하는 프롬프트(«결제 화면의 환불 버튼 고치기»)는 제목을 되풀이하는 것이 아니다.
 */
export function visibleSessionDetail(title: string, detail: string | undefined): string | undefined {
	if (!detail) return undefined;
	const name = collapseWhitespace(title);
	if ([...name].length >= MEANINGFUL_DETAIL_MIN_LENGTH && detail.startsWith(name)) return undefined;
	return detail;
}
