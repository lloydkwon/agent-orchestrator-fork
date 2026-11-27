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

	// 사이드바의 상태 칸은 mux 와 같다 — 살아 있는 상태가 있으면 그 이모지가 점 자리를 차지한다.
	// 작업 중 ⏳, 사람을 기다리면 ❗, 턴이 끝났으면 ✅. 시간 숫자는 작업 중 청록·기다림 빨강으로 칠한다.
	describe("status glyph", () => {
		const at = "2026-06-30T00:00:00Z";
		const working = { glyph: "working", symbol: "⏳", dotClassName: "", ageClassName: "text-[var(--color-term-cyan)]" };
		const needsYou = { glyph: "needs_you", symbol: "❗", dotClassName: "", ageClassName: "text-status-needs-you" };
		const done = { glyph: "done", symbol: "✅", dotClassName: "", ageClassName: "" };

		it("shows mux's hourglass while the agent works, whatever the PR says", () => {
			for (const scmStatus of [undefined, "pr_open", "mergeable", "merged"] as const) {
				expect(
					getSessionStatusGlyphView(
						sessionWith({ status: "working", scmStatus, activity: { state: "active", lastActivityAt: at } }),
					),
				).toEqual(working);
			}
		});

		it.each(["waiting_input", "blocked"] as const)(
			"shows mux's exclamation for %s even when a merged PR owns the tone",
			(state) => {
				// 점이었을 때는 병합 보라가 이겨서 사람을 기다리는 세션이 아무 신호도 내지 않았다.
				expect(
					getSessionStatusGlyphView(
						sessionWith({ status: "needs_input", scmStatus: "merged", activity: { state, lastActivityAt: at } }),
					),
				).toEqual(needsYou);
			},
		);

		it.each([
			["idle", undefined],
			["draft", "draft"],
			["mergeable", "mergeable"],
			["merged", "merged"],
			["ci_failed", "ci_failed"],
		] as const)("checks off a finished %s turn the way mux does, whatever the PR says", (status, scmStatus) => {
			// mux 처럼 ✅ 는 늘 같은 모양이다(사용자 선택, 2026-11-27) — 쉬는 세션의 PR 색은 사이드바에 남지 않는다.
			expect(
				getSessionStatusGlyphView(sessionWith({ status, scmStatus, activity: { state: "idle", lastActivityAt: at } })),
			).toEqual(done);
		});

		it("does not check off a silent no_signal session", () => {
			// 데몬은 신호 없이 유예(90초)가 지난 세션을 no_signal 로 낮추고 activity 는 idle 로 둔다 —
			// «확신 있는 idle» 을 말하지 않으려는 상태라 «끝남» 체크도 그리지 않고 예전 점으로 둔다.
			expect(
				getSessionStatusGlyphView(
					sessionWith({ status: "no_signal", activity: { state: "idle", lastActivityAt: at } }),
				),
			).toEqual({ glyph: "dot", symbol: "", dotClassName: "bg-status-needs-you", ageClassName: "" });
		});

		it("keeps the dot in its board-section tone when there is no live activity to show", () => {
			const dot = (dotClassName: string) => ({ glyph: "dot", symbol: "", dotClassName, ageClassName: "" });
			expect(getSessionStatusGlyphView(sessionWith({ status: "idle" }))).toEqual(dot("bg-status-idle"));
			expect(
				getSessionStatusGlyphView(sessionWith({ status: "exited", activity: { state: "exited", lastActivityAt: at } })),
			).toEqual(dot("bg-passive"));
			expect(
				getSessionStatusGlyphView(
					sessionWith({ status: "unknown", scmStatus: "pr_open", activity: { state: "unknown", lastActivityAt: at } }),
				),
			).toEqual(dot("bg-status-in-review"));
		});

		// LADO fork (2026-11-27): a worker that rests idle after asking its owner has an open
		// needs_input notification from the LADO watch; it shows the warning like waiting_input.
		it("warns for an idle session that waits on its owner", () => {
			const idle = sessionWith({ status: "idle", scmStatus: "pr_open", activity: { state: "idle", lastActivityAt: at } });
			expect(getSessionStatusGlyphView(idle, undefined, { awaitingOwner: true })).toEqual({
				glyph: "needs_you",
				symbol: "❗",
				dotClassName: "",
				ageClassName: "text-status-needs-you",
			});
			expect(getSessionStatusGlyphView(idle, undefined, { awaitingOwner: false }).glyph).toBe("done");
		});

		// LADO fork (2026-11-27): idle on its own background work (a background shell, Monitor or
		// subagent still runs) is not a finished turn: 💻, as mux shows it.
		it("shows 💻 for an idle session whose background work runs", () => {
			const idle = sessionWith({ status: "idle", scmStatus: "pr_open", activity: { state: "idle", lastActivityAt: at } });
			expect(getSessionStatusGlyphView(idle, undefined, { backgroundTasks: 1 })).toEqual({
				glyph: "background",
				symbol: "💻",
				dotClassName: "",
				ageClassName: "",
			});
			const quiet = sessionWith({ status: "no_signal", activity: { state: "idle", lastActivityAt: at } });
			expect(getSessionStatusGlyphView(quiet, undefined, { backgroundTasks: 2 }).glyph).toBe("background");
			expect(getSessionStatusGlyphView(idle, undefined, { backgroundTasks: 0 }).glyph).toBe("done");
		});

		it("ranks ❗ and ⏳ over 💻", () => {
			const idle = sessionWith({ status: "idle", activity: { state: "idle", lastActivityAt: at } });
			expect(getSessionStatusGlyphView(idle, undefined, { backgroundTasks: 1, awaitingOwner: true }).glyph).toBe("needs_you");
			const blocked = sessionWith({ status: "needs_input", activity: { state: "blocked", lastActivityAt: at } });
			expect(getSessionStatusGlyphView(blocked, undefined, { backgroundTasks: 1 }).glyph).toBe("needs_you");
			const active = sessionWith({ status: "working", activity: { state: "active", lastActivityAt: at } });
			expect(getSessionStatusGlyphView(active, undefined, { backgroundTasks: 1 }).glyph).toBe("working");
			const exited = sessionWith({ status: "exited", activity: { state: "exited", lastActivityAt: at } });
			expect(getSessionStatusGlyphView(exited, undefined, { backgroundTasks: 1 }).glyph).toBe("dot");
		});

		it("keeps ⏳ once the awaited session works again", () => {
			expect(
				getSessionStatusGlyphView(
					sessionWith({ status: "working", activity: { state: "active", lastActivityAt: at } }),
					undefined,
					{ awaitingOwner: true },
				).glyph,
			).toBe("working");
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
