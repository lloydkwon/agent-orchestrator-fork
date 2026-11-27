import { create } from "zustand";

export const SESSION_DETAILS_STORAGE_KEY = "ao.sidebar.session-details";

/**
 * 기억하는 개수. 끝난 세션의 줄을 따로 지우지 않고 개수로 자른다. 순서는 줄이 «바뀐» 때만 새로 잡으므로,
 * 같은 줄을 오래 보이는 세션은 그 뒤 다른 세션 200개가 기록되면 밀려난다.
 */
export const MAX_REMEMBERED_SESSION_DETAILS = 200;

export function readStoredSessionDetails(): Record<string, string> {
	try {
		const saved: unknown = JSON.parse(window.localStorage.getItem(SESSION_DETAILS_STORAGE_KEY) ?? "null");
		if (saved && typeof saved === "object" && !Array.isArray(saved)) {
			return Object.fromEntries(
				Object.entries(saved).filter((entry): entry is [string, string] => typeof entry[1] === "string"),
			);
		}
	} catch {}
	return {};
}

function storeSessionDetails(details: Record<string, string>): void {
	try {
		window.localStorage.setItem(SESSION_DETAILS_STORAGE_KEY, JSON.stringify(details));
	} catch {
		// 저장소가 가득 찼거나 쓸 수 없으면 이번 실행 동안만 기억한다.
	}
}

type SessionDetailState = {
	/** 스크래치패드 세션마다 마지막으로 보인 줄. 열쇠는 `sessionDetailKey`. */
	details: Record<string, string>;
	remember: (key: string, detail: string) => void;
};

/**
 * LADO fork: 데몬은 세션의 «최근» 프롬프트 하나만 갖고 있다. 짧은 대답이 그것을 덮어쓴 뒤에도
 * 행은 일을 말해 주던 마지막 줄을 계속 보여야 하므로, 그 줄을 여기에 두고 다음 실행까지 남긴다.
 */
export const useSessionDetailStore = create<SessionDetailState>((set) => ({
	details: readStoredSessionDetails(),
	remember: (key, detail) =>
		set((state) => {
			if (state.details[key] === detail) return state;
			// 다시 넣으면 열쇠가 맨 뒤로 가므로, 맨 앞이 가장 오래 건드리지 않은 세션이다.
			const { [key]: _previous, ...rest } = state.details;
			const entries = [...Object.entries(rest), [key, detail] as const];
			const details = Object.fromEntries(entries.slice(-MAX_REMEMBERED_SESSION_DETAILS));
			storeSessionDetails(details);
			return { details };
		}),
}));
