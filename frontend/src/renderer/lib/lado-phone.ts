// LADO fork: phone approval (docs/LJY-PRD/adr/0014-phone-passkey-approvals.md).
// A passkey on the phone approves plans and integrations over the tailnet.
// Turning it on and enrolling a passkey go through the desktop app's main
// process, which confirms natively and signs; turning it off, opening the
// enrollment window and revoking a passkey are the safe direction and are
// plain calls.
import type { components } from "../../api/schema";
import { normalizeLadoPhoneCode } from "../../shared/lado-presence";
import { apiClient } from "./api-client";
import { aoBridge } from "./bridge";
import { LadoApiError, ladoError } from "./lado";

export type LadoPhoneStatus = components["schemas"]["LadoPhoneStatusResponse"];
export type LadoPhonePasskey = components["schemas"]["LadoPhonePasskeyResponse"];

/** The only definitions of phone approval's query keys. */
export const ladoPhoneQueryRoot = ["lado", "phone"] as const;
export const ladoPhoneStatusQueryKey = [...ladoPhoneQueryRoot, "status"] as const;
export const ladoPhonePasskeysQueryKey = [...ladoPhoneQueryRoot, "passkeys"] as const;

export { normalizeLadoPhoneCode };

export async function fetchLadoPhoneStatus(): Promise<LadoPhoneStatus> {
	const { data, error } = await apiClient.GET("/api/v1/lado/phone/status");
	if (error) throw ladoError(error, "Could not read phone approval");
	return data;
}

export async function fetchLadoPhonePasskeys(): Promise<LadoPhonePasskey[]> {
	const { data, error } = await apiClient.GET("/api/v1/lado/phone/passkeys");
	if (error) throw ladoError(error, "Could not read the phone passkeys");
	return data.passkeys;
}

/**
 * Turns phone approval on. In the desktop app the main process asks the
 * developer and signs; null means the developer cancelled. In a browser the
 * call goes to the daemon directly, which refuses it without the desktop
 * app's proof.
 */
export async function enableLadoPhone(): Promise<LadoPhoneStatus | null> {
	// The bridge answers null when there is no desktop main process to ask.
	const result = (await aoBridge.lado?.presence?.enablePhoneApproval?.()) ?? null;
	if (result) {
		if (result.status === "cancelled") return null;
		if (result.status === "error") throw new LadoApiError(result.message, result.code, result.details);
		return result.phone;
	}
	const { data, error } = await apiClient.POST("/api/v1/lado/phone/enable");
	if (error) throw ladoError(error, "Could not turn on phone approval");
	return data;
}

export async function disableLadoPhone(): Promise<LadoPhoneStatus> {
	const { data, error } = await apiClient.POST("/api/v1/lado/phone/disable");
	if (error) throw ladoError(error, "Could not turn off phone approval");
	return data;
}

/** Opens the 10-minute window in which a phone can make a passkey. */
export async function openLadoPhoneEnrollWindow(): Promise<LadoPhoneStatus> {
	const { data, error } = await apiClient.POST("/api/v1/lado/phone/enroll-window");
	if (error) throw ladoError(error, "Could not start the phone enrollment");
	return data;
}

/**
 * Enrolls the passkey a phone made, by the code the phone shows. Only the
 * desktop app can: its main process looks the passkey up, shows the phone's
 * facts and signs. null means the developer cancelled.
 */
export async function enrollLadoPhonePasskey(code: string): Promise<LadoPhonePasskey | null> {
	if (!normalizeLadoPhoneCode(code)) throw new LadoApiError("Type the eight digits the phone shows.", "LADO_PHONE_CODE_INVALID");
	const result = (await aoBridge.lado?.presence?.enrollPhonePasskey?.(code)) ?? null;
	if (!result) throw new LadoApiError("Do this in the AO desktop app.", "LADO_PRESENCE_REQUIRED");
	if (result.status === "cancelled") return null;
	if (result.status === "error") throw new LadoApiError(result.message, result.code, result.details);
	return result.passkey;
}

/** Revokes a passkey; false when it was revoked already. */
export async function revokeLadoPhonePasskey(id: string): Promise<boolean> {
	const { data, error } = await apiClient.DELETE("/api/v1/lado/phone/passkeys/{passkeyId}", {
		params: { path: { passkeyId: id } },
	});
	if (error) throw ladoError(error, "Could not revoke the phone passkey");
	return data.revoked;
}
