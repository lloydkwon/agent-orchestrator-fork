-- LADO fork: approving plans and integrations with a passkey on the
-- developer's phone, reached over the tailnet.
--
-- lado_presence_proofs.via says how a proof reached LADO: '' for the
-- desktop app's presence proof and for development proofs,
-- 'passkey:<fingerprint>' for a proof made from a phone passkey's assertion.
-- A development proof never names a passkey.
--
-- lado_phone_settings is one row (id 1). enabled says whether phone approval
-- is on; turning it on fixes the WebAuthn relying party id and origin, the
-- tailnet login that owns it, the WebAuthn user handle, the tailscale serve
-- port and the Unix socket serve forwards to, and turning it off keeps them.
-- enable_proof_nonce names the presence proof (phone_approval.on) that last
-- turned it on: the row cannot be on without one, nor without its relying
-- party, origin and owner.
--
-- lado_phone_passkeys are the enrolled passkeys. credential_id holds the
-- WebAuthn credential id's bytes, credential_json the credential as the
-- phone approval service encodes it. sign_count and clone_warning follow the
-- passkey's last use, and a clone warning, once set, stays. fingerprint
-- names the passkey in audit actors ('human:passkey:<fingerprint>'). A
-- revoked passkey keeps its row, so its credential cannot be enrolled again.
-- enroll_proof_nonce names the desktop presence proof
-- (phone_passkey.enroll) that enrolled it, one passkey per proof.
--
-- Nothing here publishes change events yet: their triggers, when the
-- settings page needs them, live in lado_migrate.go, never here.

-- +goose Up
ALTER TABLE lado_presence_proofs ADD COLUMN via TEXT NOT NULL DEFAULT ''
    CHECK (via = '' OR (dev = 0 AND substr(via, 1, 8) = 'passkey:' AND length(via) > 8));

CREATE TABLE lado_phone_settings (
    id                 INTEGER PRIMARY KEY CHECK (id = 1),
    enabled            INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    rp_id              TEXT NOT NULL DEFAULT '',
    origin             TEXT NOT NULL DEFAULT '',
    owner_login        TEXT NOT NULL DEFAULT '',
    user_handle        BLOB,
    serve_port         INTEGER NOT NULL DEFAULT 0 CHECK (serve_port BETWEEN 0 AND 65535),
    socket_path        TEXT NOT NULL DEFAULT '',
    enable_proof_nonce TEXT REFERENCES lado_presence_proofs (nonce),
    updated_at         TIMESTAMP NOT NULL,
    CHECK (enabled = 0 OR (enable_proof_nonce IS NOT NULL AND rp_id <> '' AND origin <> '' AND owner_login <> ''))
);

CREATE TABLE lado_phone_passkeys (
    id                 TEXT PRIMARY KEY CHECK (id <> ''),
    credential_id      BLOB NOT NULL UNIQUE CHECK (typeof(credential_id) = 'blob' AND length(credential_id) > 0),
    credential_json    TEXT NOT NULL CHECK (json_valid(credential_json)),
    rp_id              TEXT NOT NULL CHECK (rp_id <> ''),
    device_hint        TEXT NOT NULL DEFAULT '',
    login              TEXT NOT NULL CHECK (login <> ''),
    fingerprint        TEXT NOT NULL UNIQUE CHECK (fingerprint <> ''),
    sign_count         INTEGER NOT NULL DEFAULT 0 CHECK (sign_count >= 0),
    clone_warning      INTEGER NOT NULL DEFAULT 0 CHECK (clone_warning IN (0, 1)),
    created_at         TIMESTAMP NOT NULL,
    last_used_at       TIMESTAMP,
    revoked_at         TIMESTAMP,
    enroll_proof_nonce TEXT NOT NULL UNIQUE REFERENCES lado_presence_proofs (nonce)
);

-- +goose Down
DROP TABLE lado_phone_passkeys;
DROP TABLE lado_phone_settings;
ALTER TABLE lado_presence_proofs DROP COLUMN via;
