# LADO 14. Plans and integrations can be approved with a passkey on the developer's phone

Date: 2026-11-24
Status: Accepted. Decided by the owner on 2026-11-24 (plan "LADO phone passkey approval"). Amends [adr/0002](0002-human-presence-approval-channel.md): plan and integration approvals no longer need the desktop app when a passkey enrolled from it signs them. Adds one exception to `AGENTS.md:109` ("Do not add any other network-facing bind").

## Context

LADO's human approvals come only from the desktop app's main process, which signs `X-LADO-Presence` with a key derived in memory from the per-launch desktop token ([adr/0002](0002-human-presence-approval-channel.md)). The developer often works away from the desk, over ssh and from a phone. While they are away, every LADO task stops at plan approval or integration approval, which is why another tool stayed the main one for daily work.

The goal is to approve plans and integrations with a passkey that lives on the phone (Face ID or a fingerprint), without weakening T1 ([adr/0001](0001-threat-model-t1-first.md)): an agent making a mistake must not be able to approve its own work.

Facts checked before deciding (2026-11-24, tailscale 1.104.7):

- `/api/v1/lado` is blocked on the LAN listener (`backend/internal/httpd/lan_listener.go:86`), so AO Mobile cannot reach any LADO route, and it has no presence mechanism.
- `AGENTS.md:108-109`: the loopback listener stays bound to `127.0.0.1` and unauthenticated; the LAN listener is the only other network-facing bind.
- Agents run as the developer's own uid (1000). Anything they can reach over TCP on this machine, they can call. tailscaled runs as root (uid 0); the developer is tailscale's operator, so the same user can change `tailscale serve`.
- `tailscale serve --https=<port> unix:<path>` forwards a tailnet HTTPS port to a Unix socket. Measured with a temporary socket on port 8445, removed right after:
  - The socket's peer (SO_PEERCRED) is tailscaled, uid 0.
  - The request's `Host` becomes `localhost`; the address the phone opened comes as `X-Forwarded-Host: <MagicDNS name>:<port>`.
  - `Tailscale-User-Login`, `Tailscale-User-Name`, `X-Forwarded-For`, `X-Forwarded-Host` and `X-Forwarded-Proto` sent by the client are overwritten with the real values, and a forged `Tailscale-Funnel-Request` is removed. `Origin` passes as sent.
  - A request from this machine itself arrives with `X-Forwarded-For` set to the machine's own tailnet address.
  - An AF_UNIX path is limited to 108 bytes.
- The approvals already take a proof value: `ladotask.Service.ApprovePlan` (`backend/internal/service/ladotask/plans.go:217`) and `ladoverify.Service.ApproveIntegration` (`backend/internal/service/ladoverify/approve.go:143`), whose `Allow` hook (`approve.go:64`, `:207`) can refuse before anything is recorded. The proof is verified by `ladopresence.Verifier.Verify` (`backend/internal/service/ladopresence/presence.go:100`), and the desktop app confirms in a native dialog (`frontend/src/main/lado-presence.ts`, `confirm`).
- `github.com/go-webauthn/webauthn` v0.18.2 implements the relying party; the developer's own `mux` project already runs passkey login behind `tailscale serve` (MIT, parts moved with `ladophone/LICENSE.mux`).

## Decision

1. **Transport without TCP.** The daemon listens on a Unix socket, `<state>/lado/phone/phone.sock` (folder 0700, socket 0600), and `tailscale serve --bg --https=8444 unix:<path>` forwards the tailnet port to it. Accept reads the peer's uid with SO_PEERCRED and keeps the connection only when it is the uid that owns tailscaled's socket and not the daemon's own uid; turning phone approval on is refused when tailscaled runs as the developer's uid (`LADO_PHONE_TAILSCALED_SAME_USER`). An agent of this machine therefore cannot connect to the socket. Linux only; elsewhere turning it on is refused (`LADO_PHONE_UNSUPPORTED`).
2. **A gate on every request.** `X-Forwarded-Host` must be the `<MagicDNS name>:8444` fixed when phone approval was turned on (the measured `Host` is `localhost`); `X-Forwarded-Proto` must be `https`; a Funnel request is refused; `Tailscale-User-Login` must be the owner recorded when it was turned on; an `X-Forwarded-For` that names this machine's own tailnet address is refused, which stops `curl` from this machine through tailscale; a write must carry the exact `Origin` and a JSON body. Every answer gets a CSP without inline code, `frame-ancestors 'none'`, no-referrer, nosniff and no-store. Only listed paths are served; the shared API router is not mounted.
3. **Enrollment: the code is only on the phone.** The desktop app opens a 10-minute window (no presence needed). The phone opens `https://<MagicDNS name>:8444/enroll` and makes a passkey (user verification required, resident key preferred, no attestation). The daemon keeps the credential **in memory only** and shows an 8-digit code and a fingerprint (the first 8 hex characters of the credential id's sha256) on the phone only. The developer types the code into the desktop app; main looks the pending passkey up with it (five wrong codes close the window), shows the device hint, the tailnet login, the time and the fingerprint in a native dialog, and on confirm signs `phone_passkey.enroll` with the credential's sha256 as subject. The daemon refuses a development proof and enrolls only when exactly one passkey is pending, writing the proof, the credential and the audit event in one transaction. The code and its hash are never written to a file, the database or a log.
4. **Turning it on and off.** Turning on takes a `phone_approval.on` proof from the desktop app (a development proof is refused), and only when port 8444 is free or already ours and Funnel is off. The relying party id (the MagicDNS name), the origin and the owner come from `tailscale status` and are fixed in the database. Turning off and revoking a passkey need no proof: they are the safe direction, as for Claude in Chrome.
5. **Login.** A passkey assertion (user verification required; a credential whose sign count went back is refused for good) opens a `__Host-lado-phone` cookie, 30 minutes idle, that can only read. More than 10 failures in 10 minutes pause logins for 5 minutes and notify the desktop; a failed signature of an enrolled credential is audited and notified at once.
6. **Every approval is signed.** The detail page gives a binding: for a plan `{revisionId, sha256, policySha256}`, for an integration `{session, prs[{url, head, verificationSha256, confirm}]}` (opening it runs the integration's dry run once). `begin{kind, binding}` needs the session, reads the subject again and refuses one that changed, refuses an integration with production database SQL, and allows one ceremony per session. Its challenge is `SHA256("lado-phone-approve-v1\n"+id+"\n"+bindingSha)`, and only that passkey is allowed. `finish` takes the ceremony out whether or not it succeeds, checks again within 120 seconds that phone approval is on, the passkey is not revoked and serve still points at the socket, validates the assertion, and calls the existing `ApprovePlan` or `ApproveIntegration` with **the bound values** and a one-time proof `{Nonce: "pk-"+id, Via: "passkey:"+fingerprint}`. The integration's `Allow` hook refuses a head with `DBChanges()` before anything is recorded. The audit event carries the binding's sha256 and the hash of `clientDataJSON`.
7. **What the phone shows.** The daemon writes the facts and the Korean lines, every confirm code included, and filters them with the desktop's unsafe-text rule (control and direction characters), moved to Go. The phone's script sets `textContent` only. A plan's body is folded and labelled as written by the worker.
8. **The audit actor.** `LadoPresenceProof.Via` and `Actor()` give `human`, `human:dev-presence` or `human:passkey:<fingerprint>`.

Production database SQL with an integration ([adr/0013](0013-db-changes-with-integration-approval.md)), rules and settings changes stay on the desktop.

## Alternatives

- **Accept the mobile app's password as the proof.** AO Mobile's bearer password is stored on the phone and in the daemon's config, which agents of this machine can read. Any agent could then approve its own work: T1 collapses.
- **Unblock `/api/v1/lado` on the LAN listener.** The phone would reach the routes but could not sign: the presence key exists only in the desktop app's memory. Approvals would still need the desktop, or would need a weaker proof.
- **A TCP listener on loopback for `tailscale serve`.** Every process of the developer's uid can connect to `127.0.0.1`, and forging `Tailscale-User-Login` is trivial without tailscaled in between. The Unix socket with SO_PEERCRED lets only tailscaled in.

## Consequences

- `AGENTS.md:109` gets one exception: the phone listener is not a TCP bind but a 0600 Unix socket that accepts only tailscaled's uid, reached through `tailscale serve`; it serves only its own paths.
- [adr/0002](0002-human-presence-approval-channel.md)'s "There is no mobile approval until mobile has a presence mechanism" no longer holds for plans and integrations: the enrolled passkey is that mechanism. Everything else still needs the desktop app.
- **T2 is not covered.** A process that stops the daemon, takes the socket's place or the serve port, or writes the database directly can approve; that is the same class as the desktop presence today.
- **Synced passkeys.** iCloud Keychain and Google Password Manager copy a passkey to the same account's other devices; an iPhone cannot be asked for a device-bound key. The enrollment names the device hint and the fingerprint, and the list in Settings revokes any of them.
- **Linux only**, because the socket's only guard is SO_PEERCRED.
- **No push.** Nothing tells the phone that an approval waits; the developer opens the page. Text an agent wrote can still try to persuade the developer; the page only marks it as the worker's.
