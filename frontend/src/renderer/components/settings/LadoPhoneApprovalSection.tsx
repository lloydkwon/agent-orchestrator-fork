import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Loader2, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { ladoErrorText } from "../../lib/lado-errors";
import {
	disableLadoPhone,
	enableLadoPhone,
	enrollLadoPhonePasskey,
	fetchLadoPhonePasskeys,
	fetchLadoPhoneStatus,
	ladoPhonePasskeysQueryKey,
	ladoPhoneQueryRoot,
	ladoPhoneStatusQueryKey,
	normalizeLadoPhoneCode,
	openLadoPhoneEnrollWindow,
	revokeLadoPhonePasskey,
	type LadoPhonePasskey,
	type LadoPhoneStatus,
} from "../../lib/lado-phone";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Switch } from "../ui/switch";
import { QrCanvas } from "./QrCanvas";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";

const QR_SIZE = 160;
// How often the state is read again while an enrollment is open, so the
// panel goes when the window closes.
const ENROLL_REFRESH_MS = 15_000;
// The eight digits, with room for the spaces or dashes people type.
const CODE_MAX_LENGTH = 12;

/**
 * LADO fork: Settings > Mobile > Phone approval
 * (docs/LJY-PRD/adr/0014-phone-passkey-approvals.md). A passkey on the phone
 * approves plans and integrations over the tailnet. Turning it on and
 * enrolling a passkey go through the desktop app's native confirmation; the
 * switch shows only what the daemon holds, so a cancelled confirmation never
 * flips it. Nothing shows while the state loads or when it cannot be read.
 */
export function LadoPhoneApprovalSection() {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const status = useQuery({
		queryKey: ladoPhoneStatusQueryKey,
		queryFn: fetchLadoPhoneStatus,
		retry: false,
		refetchInterval: (query) => (query.state.data?.enrollWindowExpiresAt ? ENROLL_REFRESH_MS : false),
	});
	const passkeys = useQuery({ queryKey: ladoPhonePasskeysQueryKey, queryFn: fetchLadoPhonePasskeys, retry: false });
	const saveStatus = (next: LadoPhoneStatus | null) => {
		// null: the developer cancelled and nothing changed.
		if (next) queryClient.setQueryData(ladoPhoneStatusQueryKey, next);
	};
	const toggle = useMutation({
		mutationFn: (on: boolean) => (on ? enableLadoPhone() : disableLadoPhone()),
		onSuccess: saveStatus,
	});
	const openWindow = useMutation({ mutationFn: openLadoPhoneEnrollWindow, onSuccess: saveStatus });
	// The fingerprint of the passkey enrolled last; it stays after the daemon
	// closes the window, until another enrollment starts.
	const [enrolled, setEnrolled] = useState<string | null>(null);

	const current = status.data;
	if (!current) return null;
	const serving = current.enabled && current.listening;
	const windowOpen =
		serving && Boolean(current.enrollUrl) && Boolean(current.enrollWindowExpiresAt) && Date.parse(current.enrollWindowExpiresAt ?? "") > Date.now();
	const list = passkeys.data ?? [];

	const state = !current.supported
		? t("lado.phone.unsupported")
		: current.enabled && !current.listening
			? t("lado.phone.notListening")
			: current.enabled
				? t("lado.phone.on", { origin: current.origin })
				: t("lado.phone.off");

	return (
		<>
			<SettingsSection
				title={t("lado.phone.title")}
				sectionId="lado-phone-approval"
				grouped
				toolbar={
					<p className="px-(--settings-row-inset) pb-(--size-settings-section-inner-gap) text-control leading-4 text-settings-muted">
						{t("lado.phone.note")}
					</p>
				}
			>
				<SettingsRow
					label={t("lado.phone.switch")}
					description={
						<>
							<span>{state}</span>
							{toggle.isError ? (
								<span role="alert" className="block text-error">
									{ladoErrorText(t, toggle.error)}
								</span>
							) : null}
						</>
					}
				>
					<Switch
						aria-label={t("lado.phone.switch")}
						checked={current.enabled}
						disabled={!current.supported || toggle.isPending}
						onCheckedChange={(on) => {
							if (on !== current.enabled) toggle.mutate(on);
						}}
					/>
				</SettingsRow>
				{serving ? (
					<SettingsRow
						label={t("lado.phone.enrollTitle")}
						description={
							<>
								<span>{t("lado.phone.enrollHint")}</span>
								{enrolled ? (
									<span role="status" className="block">
										{t("lado.phone.enrolled", { fingerprint: enrolled })}
									</span>
								) : null}
								{openWindow.isError ? (
									<span role="alert" className="block text-error">
										{ladoErrorText(t, openWindow.error)}
									</span>
								) : null}
							</>
						}
					>
						<Button
							type="button"
							size="sm"
							variant="secondary"
							disabled={openWindow.isPending}
							onClick={() => {
								setEnrolled(null);
								openWindow.mutate();
							}}
						>
							{openWindow.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
							{t("lado.phone.enrollStart")}
						</Button>
					</SettingsRow>
				) : null}
				{windowOpen && current.enrollUrl ? (
					<EnrollPanel
						url={current.enrollUrl}
						expiresAt={current.enrollWindowExpiresAt ?? ""}
						onEnrolled={(fingerprint) => {
							setEnrolled(fingerprint);
							void queryClient.invalidateQueries({ queryKey: ladoPhoneQueryRoot });
						}}
					/>
				) : null}
			</SettingsSection>
			{current.enabled || list.length > 0 ? <PasskeyList passkeys={list} /> : null}
		</>
	);
}

/** The address the phone opens, as text and QR, and the code the phone shows. */
function EnrollPanel({ url, expiresAt, onEnrolled }: { url: string; expiresAt: string; onEnrolled: (fingerprint: string) => void }) {
	const { t, i18n } = useTranslation();
	const [code, setCode] = useState("");
	const [invalid, setInvalid] = useState(false);
	const enroll = useMutation({
		mutationFn: enrollLadoPhonePasskey,
		onSuccess: (passkey) => {
			// null: the developer cancelled; the phone's passkey still waits.
			if (!passkey) return;
			setCode("");
			onEnrolled(passkey.fingerprint);
		},
	});
	const submit = (event: FormEvent) => {
		event.preventDefault();
		enroll.reset();
		const valid = normalizeLadoPhoneCode(code) !== null;
		setInvalid(!valid);
		if (valid) enroll.mutate(code);
	};
	const closes = new Date(expiresAt).toLocaleString(i18n.language, { dateStyle: "medium", timeStyle: "short" });

	return (
		<div className="settings-row-bar flex flex-col gap-3 sm:flex-row sm:items-start">
			<div className="shrink-0 overflow-hidden rounded-md">
				<QrCanvas value={url} size={QR_SIZE} showLogo={false} data-qr-value={url} />
			</div>
			<div className="flex min-w-0 flex-1 flex-col gap-2 text-control leading-4">
				<span className="text-settings-muted">{t("lado.phone.enrollOpen", { time: closes })}</span>
				<span className="select-all break-all font-mono text-settings-label">{url}</span>
				<form className="flex flex-wrap items-center gap-2" onSubmit={submit}>
					<Input
						className="w-40 max-w-full"
						aria-label={t("lado.phone.codeLabel")}
						placeholder={t("lado.phone.codePlaceholder")}
						inputMode="numeric"
						autoComplete="one-time-code"
						maxLength={CODE_MAX_LENGTH}
						value={code}
						onChange={(event) => setCode(event.target.value)}
					/>
					<Button type="submit" size="sm" disabled={enroll.isPending}>
						{enroll.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
						{t("lado.phone.enrollConfirm")}
					</Button>
				</form>
				{invalid || enroll.isError ? (
					<span role="alert" className="text-error">
						{invalid ? t("lado.phone.codeInvalid") : ladoErrorText(t, enroll.error)}
					</span>
				) : null}
			</div>
		</div>
	);
}

/** Every enrolled passkey, revoked ones too, with a revoke button for the rest. */
function PasskeyList({ passkeys }: { passkeys: LadoPhonePasskey[] }) {
	const { t, i18n } = useTranslation();
	const queryClient = useQueryClient();
	const revoke = useMutation({
		mutationFn: revokeLadoPhonePasskey,
		onSuccess: () => void queryClient.invalidateQueries({ queryKey: ladoPhonePasskeysQueryKey }),
	});
	const time = (iso: string) => new Date(iso).toLocaleString(i18n.language, { dateStyle: "medium", timeStyle: "short" });
	const title = t("lado.phone.passkeys");

	return (
		<SettingsSection title={title} grouped>
			{passkeys.length === 0 ? (
				<div className="settings-row-bar text-caption text-settings-muted">{t("lado.phone.passkeysEmpty")}</div>
			) : (
				<div role="list" aria-label={title} className="divide-y divide-border">
					{passkeys.map((passkey) => (
						<SettingsRow
							key={passkey.id}
							role="listitem"
							icon={KeyRound}
							label={passkey.deviceHint || t("lado.phone.deviceUnknown")}
							description={
								<>
									<span className="block">
										{t("lado.phone.passkeyDetail", { fingerprint: passkey.fingerprint, login: passkey.login, created: time(passkey.createdAt) })}
									</span>
									<span className="block">
										{passkey.revoked
											? t("lado.phone.revoked", { time: passkey.revokedAt ? time(passkey.revokedAt) : "" })
											: passkey.lastUsedAt
												? t("lado.phone.lastUsed", { time: time(passkey.lastUsedAt) })
												: t("lado.phone.neverUsed")}
									</span>
									{passkey.cloneWarning ? <span className="block text-error">{t("lado.phone.cloneWarning")}</span> : null}
								</>
							}
						>
							{passkey.revoked ? null : (
								<Button
									type="button"
									size="icon-sm"
									variant="ghost"
									aria-label={t("lado.phone.revokeAria", { fingerprint: passkey.fingerprint })}
									title={t("lado.phone.revokeAria", { fingerprint: passkey.fingerprint })}
									disabled={revoke.isPending}
									onClick={() => revoke.mutate(passkey.id)}
								>
									<Trash2 aria-hidden="true" />
								</Button>
							)}
						</SettingsRow>
					))}
				</div>
			)}
			{revoke.isError ? (
				<div role="alert" className="settings-row-bar text-caption text-error">
					{ladoErrorText(t, revoke.error)}
				</div>
			) : null}
		</SettingsSection>
	);
}
