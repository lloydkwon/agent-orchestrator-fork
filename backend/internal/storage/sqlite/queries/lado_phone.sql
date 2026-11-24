-- LADO fork: phone passkey approval's settings and enrolled passkeys
-- (migrations_lado/0023_lado_phone_approval.sql).

-- name: GetLadoPhoneSettings :one
SELECT id, enabled, rp_id, origin, owner_login, user_handle, serve_port, socket_path, enable_proof_nonce, updated_at
FROM lado_phone_settings
WHERE id = 1;

-- name: EnableLadoPhoneSettings :exec
INSERT INTO lado_phone_settings (
    id, enabled, rp_id, origin, owner_login, user_handle, serve_port, socket_path, enable_proof_nonce, updated_at
)
VALUES (1, 1, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    enabled = 1,
    rp_id = excluded.rp_id,
    origin = excluded.origin,
    owner_login = excluded.owner_login,
    user_handle = excluded.user_handle,
    serve_port = excluded.serve_port,
    socket_path = excluded.socket_path,
    enable_proof_nonce = excluded.enable_proof_nonce,
    updated_at = excluded.updated_at;

-- name: DisableLadoPhoneSettings :execrows
UPDATE lado_phone_settings
SET enabled = 0, updated_at = ?
WHERE id = 1 AND enabled = 1;

-- name: InsertLadoPhonePasskey :exec
INSERT INTO lado_phone_passkeys (
    id, credential_id, credential_json, rp_id, device_hint, login, fingerprint, sign_count, created_at, enroll_proof_nonce
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListLadoPhonePasskeys :many
SELECT id, credential_id, credential_json, rp_id, device_hint, login, fingerprint, sign_count, clone_warning,
    created_at, last_used_at, revoked_at, enroll_proof_nonce
FROM lado_phone_passkeys
ORDER BY created_at, id;

-- name: GetLadoPhonePasskeyByCredentialID :one
SELECT id, credential_id, credential_json, rp_id, device_hint, login, fingerprint, sign_count, clone_warning,
    created_at, last_used_at, revoked_at, enroll_proof_nonce
FROM lado_phone_passkeys
WHERE credential_id = ?;

-- name: CountLadoPhonePasskeysByID :one
SELECT COUNT(*) FROM lado_phone_passkeys
WHERE id = ?;

-- name: RecordLadoPhonePasskeyUse :execrows
-- A clone warning, once set, stays.
UPDATE lado_phone_passkeys
SET sign_count = sqlc.arg(sign_count),
    clone_warning = MAX(clone_warning, CAST(sqlc.arg(clone_warning) AS INTEGER)),
    last_used_at = sqlc.arg(last_used_at)
WHERE id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: RevokeLadoPhonePasskey :execrows
UPDATE lado_phone_passkeys
SET revoked_at = ?
WHERE id = ? AND revoked_at IS NULL;
