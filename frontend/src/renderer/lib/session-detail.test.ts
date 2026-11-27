import { describe, expect, it } from "vitest";
import { resolveSessionDetail, sessionDetailKey, visibleSessionDetail } from "./session-detail";

describe("resolveSessionDetail", () => {
	it("takes a prompt long enough to name the work", () => {
		expect(resolveSessionDetail("이전에 하던 사이드바 작업", "결제 화면의 환불 버튼 고치기")).toBe("결제 화면의 환불 버튼 고치기");
	});

	it("keeps the remembered line when the new prompt is only a short reply", () => {
		expect(resolveSessionDetail("결제 화면의 환불 버튼 고치기", "응 진행해")).toBe("결제 화면의 환불 버튼 고치기");
	});

	it("shows a short prompt when nothing is remembered yet", () => {
		expect(resolveSessionDetail(undefined, "버그 수정")).toBe("버그 수정");
	});

	it("keeps the remembered line when the daemon has no prompt", () => {
		expect(resolveSessionDetail("결제 화면의 환불 버튼 고치기", undefined)).toBe("결제 화면의 환불 버튼 고치기");
		expect(resolveSessionDetail("결제 화면의 환불 버튼 고치기", "   ")).toBe("결제 화면의 환불 버튼 고치기");
		expect(resolveSessionDetail(undefined, undefined)).toBeUndefined();
	});

	it("counts characters, not UTF-16 units", () => {
		// Nine emoji are 18 UTF-16 units but still a short reply.
		expect(resolveSessionDetail("결제 화면의 환불 버튼 고치기", "👍👍👍👍👍👍👍👍👍")).toBe("결제 화면의 환불 버튼 고치기");
	});
});

describe("visibleSessionDetail", () => {
	it("shows a line that says more than the title", () => {
		expect(visibleSessionDetail("claude-code", "결제 화면의 환불 버튼 고치기")).toBe("결제 화면의 환불 버튼 고치기");
	});

	it("hides a line that only restates the title", () => {
		// A scratchpad agent started with a brief is titled with the brief's first 100 characters.
		expect(visibleSessionDetail("fix the login\nredirect", "fix the login redirect after session expiry")).toBeUndefined();
		expect(visibleSessionDetail("fix the login redirect", "fix the login redirect")).toBeUndefined();
	});

	it("shows a line that merely starts with a short title", () => {
		// A harness name or a short name the user chose is not a title taken from the prompt.
		expect(visibleSessionDetail("결제", "결제 화면의 환불 버튼 고치기")).toBe("결제 화면의 환불 버튼 고치기");
		expect(visibleSessionDetail("codex", "codex 설정에서 모델 바꾸기")).toBe("codex 설정에서 모델 바꾸기");
	});

	it("shows nothing without a line", () => {
		expect(visibleSessionDetail("claude-code", undefined)).toBeUndefined();
	});
});

describe("sessionDetailKey", () => {
	it("keeps the same session id on two hosts apart", () => {
		expect(sessionDetailKey({ id: "standalone-3" })).not.toBe(sessionDetailKey({ id: "standalone-3", hostId: "box-a" }));
	});

	it("keeps a reused session id apart from the session that had it before", () => {
		// A fresh data directory numbers scratchpad sessions from 1 again.
		expect(sessionDetailKey({ id: "standalone-1", createdAt: "2026-11-27T01:00:00Z" })).not.toBe(
			sessionDetailKey({ id: "standalone-1", createdAt: "2026-11-20T01:00:00Z" }),
		);
	});
});
