// LADO fork (2026-11-27): the background tasks (shells, Monitors, subagents) the live sessions'
// agents left running. A session idle on them waits on its own work: the sidebar shows 💻 and the
// kanban card says so instead of «Needs review». A session absent from the list is not known.
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "./api-client";

export type LadoSessionBackground = components["schemas"]["LadoSessionBackgroundResponse"];
export type LadoBackgroundTask = components["schemas"]["LadoBackgroundTaskResponse"];

export const ladoBackgroundQueryKey = ["lado", "background-tasks"] as const;

export async function fetchLadoBackground(signal?: AbortSignal): Promise<LadoSessionBackground[]> {
	const { data, error, response } = await apiClient.GET("/api/v1/lado/background-tasks", { signal });
	// An older daemon has no such route: nothing is known.
	if (response?.status === 404 || response?.status === 501) return [];
	if (error) throw new Error(apiErrorMessage(error, "Could not load the sessions' background tasks"));
	return data?.sessions ?? [];
}

const EMPTY: ReadonlyMap<string, LadoSessionBackground> = new Map();

/** The sessions with background work running, by session id. */
export function selectRunningBackground(list: LadoSessionBackground[] | undefined): ReadonlyMap<string, LadoSessionBackground> {
	if (!Array.isArray(list) || list.length === 0) return EMPTY;
	const running = new Map<string, LadoSessionBackground>();
	for (const session of list) {
		if (session.sessionId && Array.isArray(session.tasks) && session.tasks.length > 0) running.set(session.sessionId, session);
	}
	return running.size === 0 ? EMPTY : running;
}

/** One line for a tooltip: the first task's description, its start, and how many more run. */
export function backgroundSummary(session: LadoSessionBackground, locale?: string): string {
	const [first, ...rest] = session.tasks;
	if (!first) return "";
	const parts = [first.description || first.type];
	if (first.startedAt) {
		const at = new Date(first.startedAt);
		if (!Number.isNaN(at.getTime())) parts.push(at.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" }));
	}
	const line = parts.join(" · ");
	return rest.length > 0 ? `${line} +${rest.length}` : line;
}
