import { describe, expect, it } from "vitest";
import type { NotificationDTO, NotificationsPage } from "../lib/notifications";
import { selectAwaitingOwnerSessionIds } from "./useAwaitingOwner";

function notification(overrides: Partial<NotificationDTO>): NotificationDTO {
	return {
		id: "ntf-1",
		type: "needs_input",
		sessionId: "water_ai-52",
		projectId: "water_ai",
		prUrl: "",
		title: "water_ai-52 needs your input",
		body: "",
		status: "unread",
		createdAt: "2026-11-27T01:00:00Z",
		resolvedAt: null,
		target: { kind: "session", sessionId: "water_ai-52" },
		...overrides,
	};
}

function page(notifications: NotificationDTO[]): NotificationsPage {
	return { notifications, unreadCount: 0, unresolvedCount: notifications.length };
}

describe("selectAwaitingOwnerSessionIds", () => {
	it("keeps the sessions with an open needs_input notification", () => {
		const ids = selectAwaitingOwnerSessionIds(
			page([
				notification({ id: "a", sessionId: "water_ai-52" }),
				notification({ id: "b", sessionId: "water_ai-53", resolvedAt: "2026-11-27T01:05:00Z" }),
				notification({ id: "c", sessionId: "water_ai-54", type: "ready_to_merge" }),
				// Read is a seen state, not an answer: the session still waits.
				notification({ id: "d", sessionId: "water_ai-55", status: "read" }),
			]),
		);
		expect([...ids].sort()).toEqual(["water_ai-52", "water_ai-55"]);
	});

	it("is empty without a page", () => {
		expect(selectAwaitingOwnerSessionIds(undefined).size).toBe(0);
	});
});
