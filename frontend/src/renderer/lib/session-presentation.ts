import {
	attentionZone,
	attentionZoneOrder,
	boardAttentionZoneOrder,
	boardKanbanColumnOrder,
	getAgentActivityView as getPortableAgentActivityView,
	getAttentionZoneView as getPortableAttentionZoneView,
	getAttentionZoneViewForZone as getPortableAttentionZoneViewForZone,
	getKanbanColumnView as getPortableKanbanColumnView,
	getSessionStatusView as getPortableSessionStatusView,
	getSessionTimelinePillView as getPortableSessionTimelinePillView,
	isAgentActivityWorking,
	isSessionIdle,
	type AgentActivityView,
	type AttentionZone,
	type AttentionZoneView,
	type KanbanColumn,
	type KanbanColumnView,
	type ProductUITranslator,
	type SessionStatusView,
	type SessionTimelinePillStatus,
	type SessionTimelinePillView,
} from "@aoagents/product-ui";
import type { TFunction } from "i18next";
import { appI18n, type MessageKey } from "../i18n";
import type { SessionActivity, SessionStatus } from "../types/workspace";

function translator(t: TFunction): ProductUITranslator {
	return (key, values) => t(key as MessageKey, values);
}

export function getAgentActivityView(
	activity?: SessionActivity | null,
	t: TFunction = appI18n.t,
): AgentActivityView {
	return getPortableAgentActivityView(activity, translator(t));
}

export function getSessionStatusView(
	status: SessionStatus,
	t: TFunction = appI18n.t,
): SessionStatusView {
	return getPortableSessionStatusView(status, translator(t));
}

export function getAttentionZoneView(
	status: SessionStatus,
	t: TFunction = appI18n.t,
): AttentionZoneView {
	return getPortableAttentionZoneView(status, translator(t));
}

export function getAttentionZoneViewForZone(
	zone: AttentionZone,
	t: TFunction = appI18n.t,
): AttentionZoneView {
	return getPortableAttentionZoneViewForZone(zone, translator(t));
}

export type SessionStatusDotView = {
	className: string;
	breathe: boolean;
};

type SessionToneInput = {
	activity?: SessionActivity | null;
	displayStatus?: string;
	scmStatus?: SessionStatus;
	status: SessionStatus;
};

// The dot's `bg-*` board-section tone. Colour comes from the board section
// represented by the SCM state, which survives a running agent — `status` is
// activity-first, so it collapses to `working` the moment an agent wakes and
// would otherwise take every pull request tone with it. Merged keeps its
// split-section tone instead of sharing Ready to merge's tone. A no-PR idle
// session that starts working turns blue.
function getSessionSectionDotClassName(session: SessionToneInput, t: TFunction): string {
	const working = isAgentActivityWorking(session.activity);
	const closedWithoutMerge = session.displayStatus === "Closed without merge";
	const sectionStatus: SessionStatus =
		closedWithoutMerge ? "exited" : (session.scmStatus ?? session.status);
	const toneStatus = sectionStatus === "idle" && working ? "working" : sectionStatus;
	if (closedWithoutMerge) return getSessionStatusView("exited", t).dotClassName;
	if (toneStatus === "exited") return "bg-passive";
	if (toneStatus === "idle" || toneStatus === "merged") return getSessionStatusView(toneStatus, t).dotClassName;
	return getAttentionZoneView(toneStatus, t).dotClassName;
}

// The session dot carries two independent signals: the board-section colour
// (getSessionSectionDotClassName) and motion on raw agent activity.
export function getSessionStatusDotView(
	session: SessionToneInput,
	t: TFunction = appI18n.t,
): SessionStatusDotView {
	return {
		className: getSessionSectionDotClassName(session, t),
		breathe: isAgentActivityWorking(session.activity),
	};
}

/** What fills a sidebar row's status slot. `dot` keeps the plain section dot. */
export type SessionStatusGlyph = "working" | "needs_you" | "done" | "background" | "dot";

export type SessionStatusGlyphView = {
	glyph: SessionStatusGlyph;
	/** mux 와 같은 상태 이모지(⏳❗✅). `dot` 이면 "". */
	symbol: string;
	/** 점의 `bg-*` 구역 색. `dot` 일 때만 채운다. */
	dotClassName: string;
	/** 행의 시간 숫자 색. "" 이면 기본(흐린 회색) 그대로다. */
	ageClassName: string;
};

// mux(`tmux/aitools.go` 의 AIState.Icon)와 같은 글자다 — 사용자가 두 도구를 같은 눈으로 읽는다.
const MUX_STATE_SYMBOLS = { working: "⏳", needs_you: "❗", done: "✅", background: "💻" } as const;

// LADO fork: 사이드바 행의 상태 칸. mux(tmux 세션 매니저)와 같은 규칙이다 — 살아 있는 에이전트 상태가 있으면 그
// 이모지가 점 자리를 «대신» 차지하고, 옆에 붙지 않는다(DESIGN.md «Status glyph rules»).
//
// 깜빡이는 8px 점은 흐려진 순간에 회색 점과 구별되지 않았고(2026-11-26 사용자 보고), PR 이 있으면 구역 색이
// 이겨서 사람을 기다리는 세션이 아무 신호도 내지 않았다. 처음에는 Lucide 선 아이콘으로 바꿨는데 mux 와 달라 보여
// 알아보기 어렵다고 해서(2026-11-27) mux 의 이모지를 그대로 쓴다 — ⏳ 작업 중, ❗ 사람 기다림, ✅ 턴 끝남.
// ✅ 는 PR 과 상관없이 늘 같다(사용자 선택). 시간 숫자도 mux 목록처럼 상태 색을 입힌다 — 작업 중은 mux 와 같은
// 터미널 청록, 기다림은 빨강, 끝난 행은 흐린 기본색이다(눈의 피로).
// 활동을 모르면(옛 데몬, exited, unknown) 점 그대로다. no_signal 도 점이다 — 데몬이 신호 없이 유예가 지난
// 세션을 «확신 있는 idle» 로 말하지 않으려고 낮춘 상태인데 activity 는 idle 로 남아 있다.
export type SessionStatusGlyphOptions = {
	/**
	 * The session has an open needs_input notification. The LADO watch raises one for a worker
	 * that rests idle after asking its owner (2026-11-27), so the warning shows without
	 * waiting_input. A working session keeps ⏳: it already got its answer.
	 */
	awaitingOwner?: boolean;
	/**
	 * How many background tasks (shells, Monitors, subagents) the session's agent left running
	 * (LADO fork, 2026-11-27). Idle with one running waits on its own work: 💻, as mux shows a
	 * shell it waits on. Unknown is 0.
	 */
	backgroundTasks?: number;
};

export function getSessionStatusGlyphView(
	session: SessionToneInput,
	t: TFunction = appI18n.t,
	options: SessionStatusGlyphOptions = {},
): SessionStatusGlyphView {
	const state = session.activity?.state;
	if (options.awaitingOwner && state !== "active") {
		return { glyph: "needs_you", symbol: MUX_STATE_SYMBOLS.needs_you, dotClassName: "", ageClassName: "text-status-needs-you" };
	}
	if (state === "active") {
		return { glyph: "working", symbol: MUX_STATE_SYMBOLS.working, dotClassName: "", ageClassName: "text-[var(--color-term-cyan)]" };
	}
	if (state === "waiting_input" || state === "blocked") {
		return { glyph: "needs_you", symbol: MUX_STATE_SYMBOLS.needs_you, dotClassName: "", ageClassName: "text-status-needs-you" };
	}
	if (state === "idle" && (options.backgroundTasks ?? 0) > 0) {
		return { glyph: "background", symbol: MUX_STATE_SYMBOLS.background, dotClassName: "", ageClassName: "" };
	}
	if (state === "idle" && session.status !== "no_signal") {
		return { glyph: "done", symbol: MUX_STATE_SYMBOLS.done, dotClassName: "", ageClassName: "" };
	}
	return { glyph: "dot", symbol: "", dotClassName: getSessionSectionDotClassName(session, t), ageClassName: "" };
}

export function getKanbanColumnView(
	column: KanbanColumn,
	t: TFunction = appI18n.t,
): KanbanColumnView {
	return getPortableKanbanColumnView(column, translator(t));
}

export function getSessionTimelinePillView(
	status: SessionTimelinePillStatus,
	t: TFunction = appI18n.t,
): SessionTimelinePillView {
	return getPortableSessionTimelinePillView(status, translator(t));
}

/** Live labels for the current locale (getters re-resolve on each access). */
export const attentionZoneLabel: Record<AttentionZone, string> = {
	get merge() {
		return getAttentionZoneViewForZone("merge").label;
	},
	get action() {
		return getAttentionZoneViewForZone("action").label;
	},
	get pending() {
		return getAttentionZoneViewForZone("pending").label;
	},
	get working() {
		return getAttentionZoneViewForZone("working").label;
	},
	get done() {
		return getAttentionZoneViewForZone("done").label;
	},
};

export {
	attentionZone,
	attentionZoneOrder,
	boardAttentionZoneOrder,
	boardKanbanColumnOrder,
	isAgentActivityWorking,
	isSessionIdle,
};
export type {
	AgentActivityView,
	AttentionZone,
	AttentionZoneView,
	KanbanColumnView,
	SessionStatusView,
	SessionTimelinePillStatus,
	SessionTimelinePillView,
};
