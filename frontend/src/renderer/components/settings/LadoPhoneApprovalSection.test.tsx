import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { appI18n } from "../../i18n";
import { apiClient } from "../../lib/api-client";
import { aoBridge } from "../../lib/bridge";
import * as phone from "../../lib/lado-phone";
import { LadoPhoneApprovalSection } from "./LadoPhoneApprovalSection";

vi.mock("../../lib/lado-phone", async (original) => ({
	...(await original<typeof import("../../lib/lado-phone")>()),
	fetchLadoPhoneStatus: vi.fn(),
	fetchLadoPhonePasskeys: vi.fn(),
}));

const ORIGIN = "https://desk.tail1234.ts.net:8444";
const off: phone.LadoPhoneStatus = {
	supported: true,
	enabled: false,
	listening: false,
	origin: "",
	ownerLogin: "",
	rpId: "",
	servePort: 8444,
	pairingPending: false,
};
const on: phone.LadoPhoneStatus = {
	...off,
	enabled: true,
	listening: true,
	origin: ORIGIN,
	ownerLogin: "owner@example.com",
	rpId: "desk.tail1234.ts.net",
	servePort: 8444,
	enrollUrl: `${ORIGIN}/enroll`,
};
const passkey: phone.LadoPhonePasskey = {
	id: "lpk_1",
	fingerprint: "5e5e5e5e",
	deviceHint: "iPhone",
	login: "owner@example.com",
	rpId: "desk.tail1234.ts.net",
	createdAt: "2026-11-24T10:01:00Z",
	revoked: false,
	cloneWarning: false,
};

// What the daemon holds: a change it accepts is what the next read gets.
let status: phone.LadoPhoneStatus = off;
let passkeys: phone.LadoPhonePasskey[] = [];

const enable = () => vi.mocked(aoBridge.lado.presence.enablePhoneApproval);
const enroll = () => vi.mocked(aoBridge.lado.presence.enrollPhonePasskey);
const post = () => vi.mocked(apiClient.POST);
const phoneSwitch = () => screen.findByRole("switch", { name: "Approve on phone" });

function renderSection() {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	render(
		<QueryClientProvider client={client}>
			<LadoPhoneApprovalSection />
		</QueryClientProvider>,
	);
	return { client };
}

// LADO fork: Settings → Mobile → Phone approval (ADR 0014).
describe("LadoPhoneApprovalSection", () => {
	beforeEach(async () => {
		vi.resetAllMocks();
		await appI18n.changeLanguage("en");
		status = off;
		passkeys = [];
		vi.mocked(phone.fetchLadoPhoneStatus).mockImplementation(async () => status);
		vi.mocked(phone.fetchLadoPhonePasskeys).mockImplementation(async () => passkeys);
		vi.spyOn(aoBridge.lado.presence, "enablePhoneApproval").mockResolvedValue({ status: "cancelled" });
		vi.spyOn(aoBridge.lado.presence, "enrollPhonePasskey").mockResolvedValue({ status: "cancelled" });
		vi.spyOn(apiClient, "POST").mockImplementation((async (path: string) => {
			if (path === "/api/v1/lado/phone/disable") status = { ...status, enabled: false, listening: false, enrollUrl: undefined };
			if (path === "/api/v1/lado/phone/enroll-window") status = { ...status, enrollWindowExpiresAt: "2099-01-01T00:10:00Z" };
			return { data: status, error: undefined };
		}) as never);
		vi.spyOn(apiClient, "DELETE").mockImplementation((async () => {
			passkeys = passkeys.map((p) => ({ ...p, revoked: true, revokedAt: "2026-11-24T11:00:00Z" }));
			return { data: { revoked: true }, error: undefined };
		}) as never);
	});

	afterEach(() => {
		cleanup();
	});

	it("shows phone approval off, with what stays on the desktop, and no enrollment", async () => {
		renderSection();
		expect(await phoneSwitch()).not.toBeChecked();
		expect(screen.getByText("Phone approval")).toBeInTheDocument();
		expect(screen.getByText("Off")).toBeInTheDocument();
		expect(screen.getByText(/stay in this app/)).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Start enrollment" })).not.toBeInTheDocument();
	});

	it("turns it on through the desktop confirmation and shows the address", async () => {
		const user = userEvent.setup();
		enable().mockImplementation(async () => {
			status = on;
			return { status: "enabled", phone: on };
		});
		renderSection();
		await user.click(await phoneSwitch());

		await waitFor(() => expect(enable()).toHaveBeenCalledTimes(1));
		await waitFor(() => expect(screen.getByRole("switch", { name: "Approve on phone" })).toBeChecked());
		expect(screen.getByText(`On at ${ORIGIN}`)).toBeInTheDocument();
		expect(post()).not.toHaveBeenCalled();
	});

	it("in a browser, sends the daemon the empty object it reads", async () => {
		const user = userEvent.setup();
		// No desktop main process to ask: the call goes to the daemon directly.
		enable().mockResolvedValue(null as never);
		renderSection();
		await user.click(await phoneSwitch());
		await waitFor(() => expect(post()).toHaveBeenCalledWith("/api/v1/lado/phone/enable", { body: {} }));
	});

	it("keeps it off after a cancelled confirmation, and shows a refusal", async () => {
		const user = userEvent.setup();
		renderSection();
		await user.click(await phoneSwitch());
		await waitFor(() => expect(enable()).toHaveBeenCalled());
		await waitFor(() => expect(screen.getByRole("switch", { name: "Approve on phone" })).toBeEnabled());
		expect(screen.getByRole("switch", { name: "Approve on phone" })).not.toBeChecked();
		expect(screen.queryByRole("alert")).not.toBeInTheDocument();

		enable().mockResolvedValue({ status: "error", code: "LADO_PHONE_TAILSCALE_UNAVAILABLE", message: "Tailscale is not running." });
		await user.click(screen.getByRole("switch", { name: "Approve on phone" }));
		expect((await screen.findByRole("alert")).textContent).toBe("Tailscale is not running or did not answer.");
	});

	it("turns it off with a plain call", async () => {
		const user = userEvent.setup();
		status = on;
		renderSection();
		await user.click(await phoneSwitch());
		await waitFor(() => expect(post()).toHaveBeenCalledWith("/api/v1/lado/phone/disable"));
		expect(enable()).not.toHaveBeenCalled();
		await waitFor(() => expect(screen.getByRole("switch", { name: "Approve on phone" })).not.toBeChecked());
	});

	it("says off Linux that it cannot be turned on", async () => {
		status = { ...off, supported: false };
		renderSection();
		expect(await phoneSwitch()).toBeDisabled();
		expect(screen.getByText("Phone approval works on Linux only.")).toBeInTheDocument();
	});

	it("opens enrollment, shows the address as text and QR, and enrolls by the phone's code", async () => {
		const user = userEvent.setup();
		status = on;
		enroll().mockImplementation(async () => {
			// The daemon closes the enrollment window whatever the outcome.
			passkeys = [passkey];
			status = { ...status, enrollWindowExpiresAt: undefined };
			return { status: "enrolled", passkey };
		});
		renderSection();
		await user.click(await screen.findByRole("button", { name: "Start enrollment" }));
		await waitFor(() => expect(post()).toHaveBeenCalledWith("/api/v1/lado/phone/enroll-window"));

		// The QR carries the enrollment address only.
		expect(await screen.findByText(`${ORIGIN}/enroll`)).toBeInTheDocument();
		expect(document.querySelector("canvas[data-qr-value]")?.getAttribute("data-qr-value")).toBe(`${ORIGIN}/enroll`);
		const code = screen.getByRole("textbox", { name: "Code the phone shows" });
		await user.type(code, "1234 567");
		const submit = screen.getByRole("button", { name: "Enroll" });
		await user.click(submit);
		expect(await screen.findByRole("alert")).toHaveTextContent("Type the 8 digits the phone shows.");
		expect(enroll()).not.toHaveBeenCalled();

		await user.type(code, "8");
		await user.click(submit);
		await waitFor(() => expect(enroll()).toHaveBeenCalledWith("1234 5678"));
		const list = await screen.findByRole("list", { name: "Enrolled passkeys" });
		expect(within(list).getByText("iPhone")).toBeInTheDocument();
		// The window closed, and the result stays on the card.
		await waitFor(() => expect(screen.queryByRole("textbox", { name: "Code the phone shows" })).not.toBeInTheDocument());
		expect(screen.getByRole("status")).toHaveTextContent("Passkey 5e5e5e5e enrolled.");
	});

	it("lists the passkeys with their facts and revokes one", async () => {
		const user = userEvent.setup();
		status = on;
		passkeys = [
			passkey,
			{ ...passkey, id: "lpk_2", fingerprint: "0badc0de", deviceHint: "", cloneWarning: true, lastUsedAt: "2026-11-24T10:30:00Z" },
		];
		renderSection();
		const list = await screen.findByRole("list", { name: "Enrolled passkeys" });
		expect(within(list).getAllByRole("listitem")).toHaveLength(2);
		expect(within(list).getByText(/Fingerprint 5e5e5e5e · owner@example.com/)).toBeInTheDocument();
		expect(within(list).getByText("Unknown device")).toBeInTheDocument();
		expect(within(list).getByText(/may have been copied/)).toBeInTheDocument();
		expect(within(list).getByText(/Not used yet/)).toBeInTheDocument();

		await user.click(within(list).getByRole("button", { name: "Revoke the passkey 5e5e5e5e" }));
		await waitFor(() =>
			expect(apiClient.DELETE).toHaveBeenCalledWith("/api/v1/lado/phone/passkeys/{passkeyId}", { params: { path: { passkeyId: "lpk_1" } } }),
		);
		await waitFor(() => expect(within(list).queryByRole("button", { name: "Revoke the passkey 5e5e5e5e" })).not.toBeInTheDocument());
		expect(within(list).getAllByText(/^Revoked /).length).toBeGreaterThan(0);
	});
});
