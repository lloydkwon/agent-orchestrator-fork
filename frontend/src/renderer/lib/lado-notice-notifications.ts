// LADO fork: the desktop notification of an orchestrator notice. LADO tells a
// project's orchestrator what happened to its tasks one line at a time
// (docs/LJY-PRD/TDD.md §5.1); a notice's INSERT event also shows the line
// here, once, with the id lado-notice:<noticeId>, so a person away from the
// orchestrator's conversation still sees it. Lines a person has to act on
// (an approval, a failure, a production incident) are banners; the rest only
// go to the notification list. A watch alert's notice shows nothing here: the
// alert shows as its own notification (lado-alert-notifications). The ids
// shown are kept in a ring in localStorage, which every window of the app
// shares, so a replayed event, a reload or a second window shows nothing
// again. A click on one opens its project's board.
import { appI18n } from "../i18n/instance";
import { aoBridge } from "./bridge";
import { fetchLadoNotice, type LadoNotice } from "./lado";

export const LADO_NOTICE_NOTIFICATION_PREFIX = "lado-notice:";
/** The localStorage key of the ring of shown notices. */
export const LADO_NOTICE_RING_KEY = "ao.lado.shownNotices";
/** How many shown notices the ring remembers. */
export const LADO_NOTICE_RING_SIZE = 512;
/**
 * A notice queued longer ago than this when its event arrives does not show:
 * a daemon from before the stream cursor replays its whole log on connect.
 */
export const LADO_NOTICE_FRESH_MS = 15 * 60_000;
/** The line's prefix, which the notification's title replaces. */
const LADO_NOTICE_LINE_PREFIX = "LADO 알림: ";

/** A new notice as its INSERT event names it. */
export type LadoNoticeEvent = { noticeId: number; projectId: string };

/** The notification type main gives a critical (banner) or a normal toast. */
export type LadoNoticeToastType = "lado_notice_urgent" | "lado_notice";

/** Where a shown notice leads; the ring keeps it so a click after a reload still opens its board. */
type ShownNotice = { id: number; projectId: string };

/**
 * The kinds (a notice key's part before its first colon) a person has to act
 * on: a plan revision or an integration waiting for approval, a plan or a
 * run that failed, a production log incident, and every production database
 * change notice, which each asks for the app.
 */
const URGENT_KINDS: ReadonlySet<string> = new Set([
	"revision",
	"integration",
	"plan_failed",
	"failed",
	"entry_failed",
	"log",
	"log_burst",
	"log_source_down",
]);

/** Which toast a notice key gets, or null for a watch alert's notice. */
export function ladoNoticeToastType(key: string): LadoNoticeToastType | null {
	const kind = key.split(":", 1)[0] ?? "";
	if (kind === "alert") return null;
	if (URGENT_KINDS.has(kind) || kind.startsWith("db_")) return "lado_notice_urgent";
	return "lado_notice";
}

function storage(): Storage | undefined {
	try {
		return typeof window === "undefined" ? undefined : window.localStorage;
	} catch {
		return undefined;
	}
}

function readRing(): ShownNotice[] {
	const raw = storage()?.getItem(LADO_NOTICE_RING_KEY);
	if (!raw) return [];
	try {
		const parsed: unknown = JSON.parse(raw);
		if (!Array.isArray(parsed)) return [];
		return parsed.filter(
			(entry): entry is ShownNotice =>
				typeof entry === "object" &&
				entry !== null &&
				typeof (entry as ShownNotice).id === "number" &&
				typeof (entry as ShownNotice).projectId === "string",
		);
	} catch {
		return [];
	}
}

function writeRing(entries: readonly ShownNotice[]): void {
	try {
		storage()?.setItem(LADO_NOTICE_RING_KEY, JSON.stringify(entries.slice(-LADO_NOTICE_RING_SIZE)));
	} catch {
		// A full or blocked storage shows the notice in this window anyway.
	}
}

/**
 * Records the notice as shown and says whether this call was first. The ring
 * is read again right before the write, so of two windows the later one sees
 * the earlier one's entry.
 */
function claimLadoNotice(entry: ShownNotice): boolean {
	const ring = readRing();
	if (ring.some((shown) => shown.id === entry.id)) return false;
	writeRing([...ring, entry]);
	return true;
}

type NotifyDeps = {
	fetchNotice: (noticeId: number) => Promise<LadoNotice | null>;
	show: (notification: { id: string; title: string; body?: string; type?: string }) => Promise<void> | void;
	now: () => number;
};

const defaultDeps: NotifyDeps = {
	fetchNotice: fetchLadoNotice,
	show: (notification) => aoBridge.notifications.show(notification),
	now: () => Date.now(),
};

/**
 * Shows each new notice once across windows, oldest first, so the newest
 * toast lands on top. A notice already shown, gone, too old or of a watch
 * alert shows nothing; one that could not be read is not recorded, so a later
 * event of it may still show it.
 */
export async function showLadoNoticeNotifications(events: readonly LadoNoticeEvent[], deps: NotifyDeps = defaultDeps): Promise<void> {
	const shown = new Set(readRing().map((entry) => entry.id));
	const ids = [...new Set(events.map((event) => event.noticeId))].filter((id) => !shown.has(id)).sort((a, b) => a - b);
	for (const id of ids) {
		let notice: LadoNotice | null;
		try {
			notice = await deps.fetchNotice(id);
		} catch {
			continue;
		}
		if (!notice) continue;
		const type = ladoNoticeToastType(notice.key);
		if (!type) continue;
		if (deps.now() - Date.parse(notice.createdAt) > LADO_NOTICE_FRESH_MS) continue;
		if (!claimLadoNotice({ id: notice.id, projectId: notice.projectId })) continue;
		const body = notice.text.startsWith(LADO_NOTICE_LINE_PREFIX) ? notice.text.slice(LADO_NOTICE_LINE_PREFIX.length) : notice.text;
		void deps.show({
			id: `${LADO_NOTICE_NOTIFICATION_PREFIX}${notice.id}`,
			title: appI18n.t("lado.notice.notificationTitle", { project: notice.projectId }),
			body,
			type,
		});
	}
}

/** Shows the project's board. */
function showProjectBoard(projectId: string): void {
	const hash = `#/projects/${encodeURIComponent(projectId)}`;
	if (window.location.hash !== hash) window.location.hash = hash;
}

/**
 * Listens for clicks on LADO's notice notifications and opens the notice's
 * project board. Other ids are AO's or the alerts' and left to their
 * listeners.
 */
export function listenLadoNoticeClicks(navigate: (projectId: string) => void = showProjectBoard): () => void {
	return aoBridge.notifications.onClick((id) => {
		if (!id.startsWith(LADO_NOTICE_NOTIFICATION_PREFIX)) return;
		const noticeId = Number(id.slice(LADO_NOTICE_NOTIFICATION_PREFIX.length));
		const shown = readRing().find((entry) => entry.id === noticeId);
		if (!shown) return;
		navigate(shown.projectId);
	});
}
