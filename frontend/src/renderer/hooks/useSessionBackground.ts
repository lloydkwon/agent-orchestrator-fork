import { useQuery } from "@tanstack/react-query";
import { fetchLadoBackground, ladoBackgroundQueryKey, selectRunningBackground, type LadoSessionBackground } from "../lib/lado-background";

const NONE: ReadonlyMap<string, LadoSessionBackground> = new Map();

// LADO fork (2026-11-27): the sessions idle on their own background work. The change stream
// invalidates the list (lado-events.ts, kind lado_background); the interval only covers a missed
// event.
export function useRunningBackground(enabled = true): ReadonlyMap<string, LadoSessionBackground> {
	const query = useQuery({
		queryKey: ladoBackgroundQueryKey,
		queryFn: ({ signal }) => fetchLadoBackground(signal),
		select: selectRunningBackground,
		refetchInterval: 60_000,
		retry: false,
		enabled,
	});
	return query.data ?? NONE;
}

/** This session's running background work; the list is this daemon's, so another host's session has none. */
export function useSessionBackground(session: { id: string; hostId?: string }): LadoSessionBackground | undefined {
	// Another host's session never asks this daemon.
	const running = useRunningBackground(!session.hostId);
	return session.hostId ? undefined : running.get(session.id);
}
