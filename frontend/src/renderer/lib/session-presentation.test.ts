import { describe, expect, it } from "vitest";
import {
	attentionZone,
	getAgentActivityView,
	getAttentionZoneView,
	getSessionStatusDotView,
	getSessionStatusGlyphView,
	getSessionStatusView,
	getSessionTimelinePillView,
	isAgentActivityWorking,
	isSessionIdle,
} from "./session-presentation";
import type { WorkspaceSession } from "../types/workspace";

function sessionWith(overrides: Partial<WorkspaceSession>): WorkspaceSession {
	return {
		id: "sess-1",
		workspaceId: "ws-1",
		workspaceName: "my-app",
		title: "fix-bug",
		provider: "claude-code",
		branch: "feat/x",
		status: "working",
		updatedAt: "2026-01-01T00:00:00Z",
		prs: [],
		...overrides,
	};
}

const openPr: WorkspaceSession["prs"][number] = {
	number: 7,
	url: "https://github.com/acme/app/pull/7",
	state: "open",
	ci: "unknown",
	review: "none",
	mergeability: "unknown",
	reviewComments: false,
	updatedAt: "2026-01-01T00:00:00Z",
};

describe("session presentation", () => {
	it.each([
		["active", "Working", true, "bg-status-working animate-status-pulse"],
		["idle", "Idle", false, "bg-status-idle"],
		["waiting_input", "Input Needed", false, "bg-status-needs-you"],
		["blocked", "Awaiting Decision", false, "bg-status-needs-you"],
		["exited", "Exited", false, "bg-status-exited"],
		["unknown", "Unknown", false, "bg-status-unknown"],
	] as const)("maps %s agent activity to %s", (state, label, breathe, indicatorClassName) => {
		expect(getAgentActivityView({ state, lastActivityAt: "" })).toMatchObject({
			label,
			breathe,
			indicatorClassName,
		});
	});

	it("uses raw agent activity, not session status, for working indicators", () => {
		expect(isAgentActivityWorking({ state: "active", lastActivityAt: "" })).toBe(true);
		expect(isAgentActivityWorking({ state: "idle", lastActivityAt: "" })).toBe(false);
		expect(isAgentActivityWorking(undefined)).toBe(false);
	});

	it.each([
		["working", "Working"],
		["idle", "Idle"],
		["needs_input", "Input needed"],
		["no_signal", "No signal"],
		["ci_failed", "CI failed"],
		["changes_requested", "Changes requested"],
		["review_pending", "Review pending"],
		["draft", "Draft PR"],
		["pr_open", "PR open"],
		["approved", "Approved"],
		["mergeable", "Ready"],
		["merged", "Merged"],
		["exited", "Exited"],
		["terminated", "Terminated"],
		["unknown", "Unknown status"],
	] as const)("maps %s session status to %s", (status, label) => {
		expect(getSessionStatusView(status).label).toBe(label);
	});

	it("uses distinct session-card tones for idle, no signal, and PR waiting states", () => {
		expect(getSessionStatusView("idle").className).toBe("text-status-idle");
		expect(getSessionStatusView("no_signal").className).toBe("text-status-unknown");
		expect(getSessionStatusView("draft").className).toBe("text-status-in-review");
		expect(getSessionStatusView("pr_open").className).toBe("text-status-in-review");
		expect(getSessionStatusView("review_pending").className).toBe("text-status-in-review");
		expect(getSessionStatusView("exited").className).toBe("text-status-exited");
	});

	it.each([
		["approved", "merge", "Ready to merge"],
		["mergeable", "merge", "Ready to merge"],
		["needs_input", "action", "Needs you"],
		["exited", "action", "Needs you"],
		["no_signal", "action", "Needs you"],
		["ci_failed", "action", "Needs you"],
		["changes_requested", "action", "Needs you"],
		["unknown", "action", "Needs you"],
		["review_pending", "pending", "In review"],
		["pr_open", "pending", "In review"],
		["draft", "pending", "In review"],
		["working", "working", "Working"],
		["idle", "working", "Working"],
		["merged", "merge", "Ready to merge"],
		["terminated", "done", "Terminated"],
	] as const)("maps %s to the %s attention zone", (status, zone, label) => {
		expect(attentionZone(sessionWith({ status }))).toBe(zone);
		expect(getAttentionZoneView(status)).toMatchObject({ zone, label });
	});

	it.each([
		["idle", "bg-status-idle"],
		["working", "bg-status-working"],
		["needs_input", "bg-status-needs-you"],
		["exited", "bg-passive"],
		["no_signal", "bg-status-needs-you"],
		["ci_failed", "bg-status-needs-you"],
		["changes_requested", "bg-status-needs-you"],
		["unknown", "bg-status-needs-you"],
		["draft", "bg-status-in-review"],
		["pr_open", "bg-status-in-review"],
		["review_pending", "bg-status-in-review"],
		["approved", "bg-status-ready"],
		["mergeable", "bg-status-ready"],
		["merged", "bg-status-merged"],
		["terminated", "bg-status-terminated"],
	] as const)("paints the %s session dot with its board-section tone", (status, dotClassName) => {
		expect(getSessionStatusDotView(sessionWith({ status }))).toMatchObject({ className: dotClassName });
	});

	it("prefers SCM state over runtime status for the dot tone", () => {
		// A running agent drives status to `working`, which would erase every PR
		// tone in the sidebar. SCM state wins so the row keeps saying "merged".
		const merged = sessionWith({
			status: "working",
			scmStatus: "merged",
			activity: { state: "active", lastActivityAt: "" },
		});

		expect(getSessionStatusDotView(merged)).toEqual({ className: "bg-status-merged", breathe: true });
	});

	it("keeps board-section color while raw working activity starts the motion", () => {
		const scmStatus = "mergeable" as const;

		expect(
			getSessionStatusDotView(sessionWith({ status: "idle", scmStatus, activity: { state: "idle", lastActivityAt: "" } })),
		).toEqual({ className: "bg-status-ready", breathe: false });
		expect(
			getSessionStatusDotView(
				sessionWith({ status: "working", scmStatus, activity: { state: "active", lastActivityAt: "" } }),
			),
		).toEqual({ className: "bg-status-ready", breathe: true });
	});

	it("uses a blinking blue dot when an idle-section session starts working", () => {
		expect(
			getSessionStatusDotView(
				sessionWith({ status: "idle", activity: { state: "active", lastActivityAt: "" } }),
			),
		).toEqual({ className: "bg-status-working", breathe: true });
	});

	// 사이드바의 상태 칸은 mux 의 규칙을 따른다 — 살아 있는 상태가 있으면 그 모양이 점 자리를 차지한다.
	// 작업 중은 깜빡이는 점이 아니라 스피너, 사람을 기다리면 경고, 턴이 끝났으면 체크다.
	describe("status glyph", () => {
		const at = "2026-06-30T00:00:00Z";

		it("shows a blue spinner while the agent works, whatever the PR says", () => {
			for (const scmStatus of [undefined, "pr_open", "mergeable", "merged"] as const) {
				expect(
					getSessionStatusGlyphView(
						sessionWith({ status: "working", scmStatus, activity: { state: "active", lastActivityAt: at } }),
					),
				).toEqual({ glyph: "working", className: "text-status-working" });
			}
		});

		it.each(["waiting_input", "blocked"] as const)(
			"shows the needs-you mark for %s even when a merged PR owns the tone",
			(state) => {
				// 점이었을 때는 병합 보라가 이겨서 사람을 기다리는 세션이 아무 신호도 내지 않았다.
				expect(
					getSessionStatusGlyphView(
						sessionWith({ status: "needs_input", scmStatus: "merged", activity: { state, lastActivityAt: at } }),
					),
				).toEqual({ glyph: "needs_you", className: "text-status-needs-you" });
			},
		);

		it.each([
			["idle", undefined, "text-status-idle"],
			["draft", "draft", "text-status-in-review"],
			["mergeable", "mergeable", "text-status-ready"],
			["merged", "merged", "text-status-merged"],
			["ci_failed", "ci_failed", "text-status-needs-you"],
		] as const)("checks off a finished %s turn in the dot's board-section tone", (status, scmStatus, className) => {
			expect(
				getSessionStatusGlyphView(sessionWith({ status, scmStatus, activity: { state: "idle", lastActivityAt: at } })),
			).toEqual({ glyph: "done", className });
		});

		it("does not check off a silent no_signal session", () => {
			// 데몬은 신호 없이 유예(90초)가 지난 세션을 no_signal 로 낮추고 activity 는 idle 로 둔다 —
			// «확신 있는 idle» 을 말하지 않으려는 상태라 «끝남» 체크도 그리지 않고 예전 점으로 둔다.
			expect(
				getSessionStatusGlyphView(
					sessionWith({ status: "no_signal", activity: { state: "idle", lastActivityAt: at } }),
				),
			).toEqual({ glyph: "dot", className: "bg-status-needs-you" });
		});

		it("keeps the dot when there is no live activity to show", () => {
			expect(getSessionStatusGlyphView(sessionWith({ status: "idle" }))).toEqual({
				glyph: "dot",
				className: "bg-status-idle",
			});
			expect(
				getSessionStatusGlyphView(sessionWith({ status: "exited", activity: { state: "exited", lastActivityAt: at } })),
			).toEqual({ glyph: "dot", className: "bg-passive" });
			expect(
				getSessionStatusGlyphView(
					sessionWith({ status: "unknown", scmStatus: "pr_open", activity: { state: "unknown", lastActivityAt: at } }),
				),
			).toEqual({ glyph: "dot", className: "bg-status-in-review" });
		});

		it("keeps a closed-without-merge session red", () => {
			expect(
				getSessionStatusGlyphView(
					sessionWith({
						status: "idle",
						displayStatus: "Closed without merge",
						activity: { state: "idle", lastActivityAt: at },
					}),
				),
			).toEqual({ glyph: "done", className: "text-status-exited" });
		});
	});

	it("paints a closed-without-merge session red", () => {
		expect(
			getSessionStatusDotView(
				sessionWith({ status: "idle", displayStatus: "Closed without merge" }),
			),
		).toEqual({ className: "bg-status-exited", breathe: false });
	});

	it("keeps activity indicator color independent from PR and CI presentation", () => {
		const active = getAgentActivityView({ state: "active", lastActivityAt: "" });
		const idle = getAgentActivityView({ state: "idle", lastActivityAt: "" });

		expect(active.indicatorClassName).toBe("bg-status-working animate-status-pulse");
		expect(idle.indicatorClassName).toBe("bg-status-idle");
	});

	it("uses a muted accent treatment for In Review instead of idle gray", () => {
		expect(getAttentionZoneView("review_pending")).toMatchObject({
			dot: "var(--color-status-in-review)",
			titleClassName: "text-status-in-review",
			dotClassName: "bg-status-in-review",
		});
	});

	it("classifies only backend-derived idle sessions for the work lane", () => {
		expect(isSessionIdle(sessionWith({ status: "idle" }))).toBe(true);
		expect(
			isSessionIdle(
				sessionWith({
					status: "idle",
					activity: { state: "active", lastActivityAt: "" },
					prs: [openPr],
				}),
			),
		).toBe(true);
		expect(
			isSessionIdle(
				sessionWith({
					status: "working",
					activity: { state: "idle", lastActivityAt: "" },
					prs: [openPr],
				}),
			),
		).toBe(false);
		expect(
			isSessionIdle(
				sessionWith({
					status: "working",
					activity: { state: "active", lastActivityAt: "" },
				}),
			),
		).toBe(false);
		expect(isSessionIdle(sessionWith({ status: "working" }))).toBe(false);
	});

	it.each([
		["no_signal", "No Signal", "var(--color-status-unknown)"],
		["ci_failed", "CI Failed", "var(--color-status-exited)"],
		["changes_requested", "Changes Requested", "var(--color-status-needs-you)"],
	] as const)("centralizes the %s timeline pill", (status, label, tone) => {
		expect(getSessionTimelinePillView(status)).toMatchObject({ label, tone, breathe: false });
	});
});
