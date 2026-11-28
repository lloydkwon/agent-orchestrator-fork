import { useQuery } from "@tanstack/react-query";
import { awaitingOwnerQueryKey, fetchNotificationsPage, type NotificationsPage } from "../lib/notifications";

// LADO fork (2026-11-27): sessions that wait on their owner. AO marks a session that is
// waiting_input or blocked by its activity, but a LADO worker that ends its turn with a question
// rests idle; the LADO watch then raises a needs_input notification for it. An open needs_input
// notification of a session is what this reads, so both kinds show the sidebar warning, and the
// mark goes when AO resolves the notification (the next turn starts, or the session ends).

const EMPTY: ReadonlySet<string> = new Set();

/** The ids of the sessions with an open needs_input notification. */
export function selectAwaitingOwnerSessionIds(page: NotificationsPage | undefined): ReadonlySet<string> {
	// A page without its list (an old daemon, a test double) marks nothing.
	if (!page || !Array.isArray(page.notifications)) return EMPTY;
	const ids = new Set<string>();
	for (const notification of page.notifications) {
		if (notification.type === "needs_input" && !notification.resolvedAt && notification.sessionId) {
			ids.add(notification.sessionId);
		}
	}
	return ids.size === 0 ? EMPTY : ids;
}

/** The sessions waiting on their owner; the live notification stream refreshes it. */
export function useAwaitingOwnerSessionIds(): ReadonlySet<string> {
	const query = useQuery({
		queryKey: awaitingOwnerQueryKey,
		queryFn: ({ signal }) => fetchNotificationsPage("unresolved", "", signal),
		select: selectAwaitingOwnerSessionIds,
		// The stream invalidates this on every change; the interval only covers a missed event.
		refetchInterval: 60_000,
		retry: false,
	});
	return query.data ?? EMPTY;
}

/**
 * Whether this session waits on its owner by an open needs_input notification. The notifications
 * are this daemon's, so a session of another host (hostId set) never does.
 */
export function useSessionAwaitingOwner(session: { id: string; hostId?: string }): boolean {
	const ids = useAwaitingOwnerSessionIds();
	return !session.hostId && ids.has(session.id);
}
