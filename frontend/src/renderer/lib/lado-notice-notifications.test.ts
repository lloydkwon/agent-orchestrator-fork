import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LadoNotice } from "./lado";

const clickListeners = vi.hoisted(() => new Set<(id: string) => void>());

vi.mock("./bridge", () => ({
	aoBridge: {
		notifications: {
			show: vi.fn(async () => undefined),
			onClick: (listener: (id: string) => void) => {
				clickListeners.add(listener);
				return () => clickListeners.delete(listener);
			},
		},
	},
}));

import {
	LADO_NOTICE_FRESH_MS,
	LADO_NOTICE_RING_KEY,
	LADO_NOTICE_RING_SIZE,
	ladoNoticeToastType,
	listenLadoNoticeClicks,
	showLadoNoticeNotifications,
} from "./lado-notice-notifications";

const NOW = Date.parse("2026-11-25T03:01:00Z");

function notice(change: Partial<LadoNotice> = {}): LadoNotice {
	return {
		id: 7,
		projectId: "water_ai",
		key: "revision:lado-task-1:1",
		text: "LADO 알림: #9 계획 리비전 1 — 칸반에서 확인",
		occurredAt: "2026-11-25T02:59:02Z",
		createdAt: "2026-11-25T03:00:00Z",
		...change,
	};
}

function deps(notices: LadoNotice[]) {
	return {
		fetchNotice: vi.fn(async (id: number): Promise<LadoNotice | null> => notices.find((n) => n.id === id) ?? null),
		show: vi.fn(),
		now: () => NOW,
	};
}

const event = (noticeId: number, projectId = "water_ai") => ({ noticeId, projectId });

beforeEach(() => {
	window.localStorage.clear();
	clickListeners.clear();
});

afterEach(() => {
	window.localStorage.clear();
});

describe("ladoNoticeToastType", () => {
	it.each([
		"revision:lado-task-1:1",
		"integration:water_ai-21:aaa",
		"plan_failed:lado-plan-request-1",
		"failed:water_ai-21",
		"entry_failed:q-1",
		"log:3:gitlab:si/water_ai#40@gitlab.codegurus.co.kr",
		"log_burst:water_ai:493",
		"log_source_down:run-1:1764000000",
		"db_failed:run-1:1764000000000",
		"db_after_deploy:run-1:1764000000000",
	])("makes %s a banner", (key) => {
		expect(ladoNoticeToastType(key)).toBe("lado_notice_urgent");
	});

	it.each([
		"request:lado-plan-request-1",
		"started:water_ai-21",
		"pr:water_ai-21:https://gitlab.codegurus.co.kr/si/water_ai/-/merge_requests/5",
		"completed:water_ai-21",
		"account_limit:water_ai:claude:1:1764000000",
		"account_released:water_ai:claude:1:1764000000",
		"some_future_kind:x",
	])("keeps %s in the list", (key) => {
		expect(ladoNoticeToastType(key)).toBe("lado_notice");
	});

	// A watch alert shows as its own notification (lado-alert-notifications).
	it("shows no notice of a watch alert", () => {
		expect(ladoNoticeToastType("alert:12")).toBeNull();
	});
});

describe("showLadoNoticeNotifications", () => {
	it("shows a new notice once across windows, titled by its project, without the LADO prefix", async () => {
		const d = deps([notice()]);
		await showLadoNoticeNotifications([event(7), event(7)], d);
		// A replayed event, or the same event in another window.
		await showLadoNoticeNotifications([event(7)], d);
		expect(d.show).toHaveBeenCalledTimes(1);
		expect(d.show).toHaveBeenCalledWith({
			id: "lado-notice:7",
			title: "LADO · water_ai",
			body: "#9 계획 리비전 1 — 칸반에서 확인",
			type: "lado_notice_urgent",
		});
		expect(d.fetchNotice).toHaveBeenCalledTimes(1);
	});

	it("shows an informational notice with its own type, oldest first", async () => {
		const d = deps([notice({ id: 9, key: "completed:water_ai-21", text: "LADO 알림: #9 완료" }), notice({ id: 8, key: "started:water_ai-21", text: "LADO 알림: #9 실행 시작" })]);
		await showLadoNoticeNotifications([event(9), event(8)], d);
		expect(d.show.mock.calls.map(([n]) => [n.id, n.body, n.type])).toEqual([
			["lado-notice:8", "#9 실행 시작", "lado_notice"],
			["lado-notice:9", "#9 완료", "lado_notice"],
		]);
	});

	it("shows nothing for an alert's notice, a stale one, one that is gone or one it could not read", async () => {
		const d = deps([
			notice({ id: 1, key: "alert:3", text: "LADO 알림: #9 작업 비용 추정 $31, 기준 $30 넘음" }),
			notice({ id: 2, createdAt: new Date(NOW - LADO_NOTICE_FRESH_MS - 1).toISOString() }),
		]);
		d.fetchNotice.mockImplementation(async (id: number) => {
			if (id === 4) throw new Error("daemon down");
			return [notice({ id: 1, key: "alert:3" }), notice({ id: 2, createdAt: new Date(NOW - LADO_NOTICE_FRESH_MS - 1).toISOString() })].find((n) => n.id === id) ?? null;
		});
		await showLadoNoticeNotifications([event(1), event(2), event(3), event(4)], d);
		expect(d.show).not.toHaveBeenCalled();
	});

	it("tries a notice it could not read again on its next event", async () => {
		const d = deps([notice()]);
		d.fetchNotice.mockRejectedValueOnce(new Error("daemon down"));
		await showLadoNoticeNotifications([event(7)], d);
		await showLadoNoticeNotifications([event(7)], d);
		expect(d.show).toHaveBeenCalledTimes(1);
	});

	it("remembers at most the ring's size of shown notices", async () => {
		const ring = Array.from({ length: LADO_NOTICE_RING_SIZE }, (_, i) => ({ id: 1000 + i, projectId: "water_ai" }));
		window.localStorage.setItem(LADO_NOTICE_RING_KEY, JSON.stringify(ring));
		await showLadoNoticeNotifications([event(7)], deps([notice()]));
		const stored = JSON.parse(window.localStorage.getItem(LADO_NOTICE_RING_KEY) ?? "[]") as { id: number }[];
		expect(stored).toHaveLength(LADO_NOTICE_RING_SIZE);
		expect(stored.at(-1)?.id).toBe(7);
		expect(stored[0]?.id).toBe(1001);
	});
});

describe("listenLadoNoticeClicks", () => {
	it("opens the board of a shown notice's project and leaves other ids alone", async () => {
		await showLadoNoticeNotifications([event(7)], deps([notice()]));
		const navigate = vi.fn();
		const stop = listenLadoNoticeClicks(navigate);
		for (const listener of clickListeners) {
			listener("lado-alert:7");
			listener("lado-notice:99");
			listener("lado-notice:7");
		}
		expect(navigate).toHaveBeenCalledTimes(1);
		expect(navigate).toHaveBeenCalledWith("water_ai");
		stop();
		expect(clickListeners.size).toBe(0);
	});
});
