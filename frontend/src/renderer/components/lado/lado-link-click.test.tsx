// LADO fork 기본값: 웹 링크를 그냥 클릭하면 시스템 브라우저(Chrome)로, 수정키를
// 누르고 클릭하면 내장 Browser 패널로 연다 (docs/LJY-PRD/TDD.md §11).
import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppBrowserLinkContext, AppLink } from "../AppLink";
import { LinkPreviewCard } from "../LinkPreviewCard";
import { aoBridge } from "../../lib/bridge";

const url = "https://example.com/lado/links";
afterEach(() => vi.restoreAllMocks());

describe("LADO web link clicks", () => {
	it("opens a plainly clicked chat link in the system browser even when the AO browser is available", () => {
		const open = vi.fn();
		const external = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		render(<AppBrowserLinkContext.Provider value={open}><AppLink href={url}>docs</AppLink></AppBrowserLinkContext.Provider>);
		fireEvent.click(screen.getByRole("link"));
		expect(external).toHaveBeenCalledExactlyOnceWith(url);
		expect(open).not.toHaveBeenCalled();
	});

	it.each([{ ctrlKey: true }, { metaKey: true }, { altKey: true }])("opens a modifier-clicked chat link in the AO browser: %j", (modifiers) => {
		const open = vi.fn();
		const external = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		render(<AppBrowserLinkContext.Provider value={open}><AppLink href={url}>docs</AppLink></AppBrowserLinkContext.Provider>);
		fireEvent.click(screen.getByRole("link"), modifiers);
		expect(open).toHaveBeenCalledExactlyOnceWith(url);
		expect(external).not.toHaveBeenCalled();
	});

	it("keeps workspace files in the AO browser on a plain click", () => {
		const open = vi.fn();
		const external = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		render(<AppLink href="docs/report.html" inAppLink={() => true} onBrowserOpen={open}>report</AppLink>);
		fireEvent.click(screen.getByRole("link"));
		expect(open).toHaveBeenCalledExactlyOnceWith("docs/report.html");
		expect(external).not.toHaveBeenCalled();
	});

	it("opens a plainly clicked preview card in the system browser and a modifier click in the AO browser", () => {
		const open = vi.fn();
		const external = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		render(<LinkPreviewCard url={url} preview={{ url, title: "Open page" }} onBrowserOpen={open} />);
		fireEvent.click(screen.getByRole("link", { name: "Open page" }));
		expect(external).toHaveBeenCalledExactlyOnceWith(url);
		expect(open).not.toHaveBeenCalled();
		fireEvent.click(screen.getByRole("link", { name: "Open page" }), { altKey: true });
		expect(open).toHaveBeenCalledExactlyOnceWith(url);
	});

	it("opens a preview card in the system browser when no AO browser is available", () => {
		const external = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		render(<LinkPreviewCard url={url} preview={{ url, title: "Open page" }} />);
		fireEvent.click(screen.getByRole("link", { name: "Open page" }));
		expect(external).toHaveBeenCalledExactlyOnceWith(url);
	});

	it("keeps a remote host's local preview card off this computer's browser", () => {
		const local = "http://localhost:3000";
		const open = vi.fn();
		const external = vi.spyOn(aoBridge.app, "openExternal").mockResolvedValue(undefined);
		const { unmount } = render(<LinkPreviewCard url={local} preview={{ url: local, title: "Dev" }} onBrowserOpen={open} ladoHostLocalOnRemoteHost />);
		fireEvent.click(screen.getByRole("link", { name: "Dev" }));
		expect(open).toHaveBeenCalledExactlyOnceWith(local);
		unmount();
		render(<LinkPreviewCard url={local} preview={{ url: local, title: "Dev" }} ladoHostLocalOnRemoteHost />);
		fireEvent.click(screen.getByRole("link", { name: "Dev" }));
		expect(external).not.toHaveBeenCalled();
	});
});
