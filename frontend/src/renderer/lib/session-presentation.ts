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

/** One board-section tone, for a dot (`dotClassName`) or a glyph (`textClassName`). */
type SessionSectionTone = {
	dotClassName: string;
	textClassName: string;
};

// Colour comes from the board section represented by the SCM state, which
// survives a running agent — `status` is activity-first, so it collapses to
// `working` the moment an agent wakes and would otherwise take every pull
// request tone with it. Merged keeps its split-section tone instead of sharing
// Ready to merge's tone. A no-PR idle session that starts working turns blue.
function getSessionSectionTone(session: SessionToneInput, t: TFunction): SessionSectionTone {
	const working = isAgentActivityWorking(session.activity);
	const closedWithoutMerge = session.displayStatus === "Closed without merge";
	const sectionStatus: SessionStatus =
		closedWithoutMerge ? "exited" : (session.scmStatus ?? session.status);
	const toneStatus = sectionStatus === "idle" && working ? "working" : sectionStatus;
	if (closedWithoutMerge) {
		const view = getSessionStatusView("exited", t);
		return { dotClassName: view.dotClassName, textClassName: view.className };
	}
	if (toneStatus === "exited") return { dotClassName: "bg-passive", textClassName: "text-passive" };
	if (toneStatus === "idle" || toneStatus === "merged") {
		const view = getSessionStatusView(toneStatus, t);
		return { dotClassName: view.dotClassName, textClassName: view.className };
	}
	const zone = getAttentionZoneView(toneStatus, t);
	return { dotClassName: zone.dotClassName, textClassName: zone.titleClassName };
}

// The session dot carries two independent signals: the board-section colour
// (getSessionSectionTone) and motion on raw agent activity.
export function getSessionStatusDotView(
	session: SessionToneInput,
	t: TFunction = appI18n.t,
): SessionStatusDotView {
	return {
		className: getSessionSectionTone(session, t).dotClassName,
		breathe: isAgentActivityWorking(session.activity),
	};
}

/** What fills a sidebar row's status slot. `dot` keeps the plain section dot. */
export type SessionStatusGlyph = "working" | "needs_you" | "done" | "dot";

export type SessionStatusGlyphView = {
	glyph: SessionStatusGlyph;
	/** A `text-*` tone for a glyph, a `bg-*` tone for the dot. */
	className: string;
};

// LADO fork: 사이드바 행의 상태 칸. mux(tmux 세션 매니저)와 같은 규칙이다 — 살아 있는 에이전트 상태가 있으면 그
// 모양이 점 자리를 «대신» 차지하고, 옆에 붙지 않는다(DESIGN.md «Status glyph rules»).
//
// 깜빡이는 8px 점은 흐려진 순간에 회색 점과 구별되지 않았고(2026-11-26 사용자 보고), PR 이 있으면 구역 색이
// 이겨서 사람을 기다리는 세션이 아무 신호도 내지 않았다. 그래서 작업 중과 사람 기다림은 PR 과 상관없이
// 상태의 모양·색을 쓰고, 턴이 끝난 세션의 체크만 점과 같은 구역 색을 이어받아 PR·병합 정보를 지킨다.
// 활동을 모르면(옛 데몬, exited, unknown) 점 그대로다. no_signal 도 점이다 — 데몬이 신호 없이 유예가 지난
// 세션을 «확신 있는 idle» 로 말하지 않으려고 낮춘 상태인데 activity 는 idle 로 남아 있다.
export function getSessionStatusGlyphView(
	session: SessionToneInput,
	t: TFunction = appI18n.t,
): SessionStatusGlyphView {
	switch (session.activity?.state) {
		case "active":
			return { glyph: "working", className: "text-status-working" };
		case "waiting_input":
		case "blocked":
			return { glyph: "needs_you", className: "text-status-needs-you" };
		case "idle":
			if (session.status !== "no_signal") {
				return { glyph: "done", className: getSessionSectionTone(session, t).textClassName };
			}
			return { glyph: "dot", className: getSessionSectionTone(session, t).dotClassName };
		default:
			return { glyph: "dot", className: getSessionSectionTone(session, t).dotClassName };
	}
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
