import { beforeEach, describe, expect, it, vi } from "vitest";
import {
	MAX_REMEMBERED_SESSION_DETAILS,
	SESSION_DETAILS_STORAGE_KEY,
	readStoredSessionDetails,
	useSessionDetailStore,
} from "./session-detail-store";

describe("session-detail-store", () => {
	beforeEach(() => {
		window.localStorage.clear();
		useSessionDetailStore.setState({ details: {} });
	});

	it("remembers a line and saves it for the next launch", () => {
		useSessionDetailStore.getState().remember(":standalone-3", "결제 화면의 환불 버튼 고치기");

		expect(useSessionDetailStore.getState().details[":standalone-3"]).toBe("결제 화면의 환불 버튼 고치기");
		expect(readStoredSessionDetails()).toEqual({ ":standalone-3": "결제 화면의 환불 버튼 고치기" });
	});

	it("starts from what the previous launch saved", async () => {
		window.localStorage.setItem(SESSION_DETAILS_STORAGE_KEY, JSON.stringify({ ":standalone-3": "결제 화면의 환불 버튼 고치기" }));
		vi.resetModules();

		const relaunched = await import("./session-detail-store");

		expect(relaunched.useSessionDetailStore.getState().details).toEqual({ ":standalone-3": "결제 화면의 환불 버튼 고치기" });
	});

	it("leaves the state object alone when the line is unchanged", () => {
		useSessionDetailStore.getState().remember(":standalone-3", "결제 화면의 환불 버튼 고치기");
		const before = useSessionDetailStore.getState().details;

		useSessionDetailStore.getState().remember(":standalone-3", "결제 화면의 환불 버튼 고치기");

		expect(useSessionDetailStore.getState().details).toBe(before);
	});

	it("drops the sessions touched longest ago once the cap is passed", () => {
		for (let index = 0; index < MAX_REMEMBERED_SESSION_DETAILS; index += 1) {
			useSessionDetailStore.getState().remember(`:standalone-${index}`, `task ${index}`);
		}
		// Touching the oldest entry moves it to the fresh end.
		useSessionDetailStore.getState().remember(":standalone-0", "task 0 again");
		useSessionDetailStore.getState().remember(":standalone-new", "newest task");

		const { details } = useSessionDetailStore.getState();
		expect(Object.keys(details)).toHaveLength(MAX_REMEMBERED_SESSION_DETAILS);
		expect(details[":standalone-0"]).toBe("task 0 again");
		expect(details[":standalone-1"]).toBeUndefined();
		expect(details[":standalone-new"]).toBe("newest task");
	});

	it("ignores stored data that is not a map of strings", () => {
		window.localStorage.setItem(SESSION_DETAILS_STORAGE_KEY, JSON.stringify({ ":a": "kept", ":b": 3, ":c": null }));
		expect(readStoredSessionDetails()).toEqual({ ":a": "kept" });

		window.localStorage.setItem(SESSION_DETAILS_STORAGE_KEY, "[1,2");
		expect(readStoredSessionDetails()).toEqual({});

		window.localStorage.setItem(SESSION_DETAILS_STORAGE_KEY, JSON.stringify(["not", "a", "map"]));
		expect(readStoredSessionDetails()).toEqual({});
	});
});
