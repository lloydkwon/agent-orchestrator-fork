package store

// LADO fork: phone passkey approval's settings and enrolled passkeys
// (migrations_lado/0023_lado_phone_approval.sql). Turning phone approval on
// and enrolling a passkey need the desktop app's presence proof, which the
// store checks again here, so no other path can write them with a
// development proof or with a proof a passkey made. Turning it off and
// revoking a passkey are the safe direction and need no proof; their audit
// actor is "api", as for Claude in Chrome turned off.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// ladoPhoneAPIActor is the audit actor of the phone approval writes that
// need no presence.
const ladoPhoneAPIActor = "api"

// LadoPhoneSettings returns phone approval's settings, the zero value while
// it was never turned on.
func (s *Store) LadoPhoneSettings(ctx context.Context) (domain.LadoPhoneSettings, error) {
	settings, err := ladoPhoneSettingsTx(ctx, s.qr)
	if err != nil {
		return domain.LadoPhoneSettings{}, fmt.Errorf("read LADO phone settings: %w", err)
	}
	return settings, nil
}

// EnableLadoPhone turns phone approval on with the settings' relying party,
// origin, owner, user handle, serve port and socket, in one transaction with
// the proof that allowed it and its audit event, and returns the settings as
// stored. Their time is the event's and their proof the proof's nonce. Only
// the desktop app's proof of domain.LadoPresencePhoneApprovalOn turns it on
// (domain.ErrLadoPhoneProofRefused otherwise), once
// (domain.ErrLadoPresenceReplayed). The event's actor is the proof's
// (LadoPresenceProof.Actor): an empty one is filled in, another refused.
func (s *Store) EnableLadoPhone(ctx context.Context, settings domain.LadoPhoneSettings, proof domain.LadoPresenceProof, event domain.LadoAuditEvent) (domain.LadoPhoneSettings, error) {
	if err := ladoPhoneDesktopProof(proof, domain.LadoPresencePhoneApprovalOn); err != nil {
		return domain.LadoPhoneSettings{}, err
	}
	event, err := ladoPhoneAuditActor(event, proof.Actor())
	if err != nil {
		return domain.LadoPhoneSettings{}, err
	}
	if err := s.writeMu.LockContext(ctx); err != nil {
		return domain.LadoPhoneSettings{}, err
	}
	defer s.writeMu.Unlock()
	var stored domain.LadoPhoneSettings
	err = s.inTx(ctx, "enable LADO phone approval", func(q *gen.Queries) error {
		if err := insertLadoPresenceProof(ctx, q, proof); err != nil {
			return err
		}
		if err := q.EnableLadoPhoneSettings(ctx, gen.EnableLadoPhoneSettingsParams{
			RpID:             settings.RPID,
			Origin:           settings.Origin,
			OwnerLogin:       settings.OwnerLogin,
			UserHandle:       settings.UserHandle,
			ServePort:        int64(settings.ServePort),
			SocketPath:       settings.SocketPath,
			EnableProofNonce: ladoNullString(proof.Nonce),
			UpdatedAt:        event.At.UTC(),
		}); err != nil {
			return err
		}
		if _, err := appendLadoAuditEventTx(ctx, q, event); err != nil {
			return err
		}
		stored, err = ladoPhoneSettingsTx(ctx, q)
		return err
	})
	if err != nil {
		return domain.LadoPhoneSettings{}, unwrapLadoPhoneSentinel(err)
	}
	return stored, nil
}

// DisableLadoPhone turns phone approval off, keeping what turning it on
// fixed, with its audit event by "api" at the event's time, and returns the
// settings and whether this turned it off. Turning off what is off changes
// and records nothing.
func (s *Store) DisableLadoPhone(ctx context.Context, event domain.LadoAuditEvent) (domain.LadoPhoneSettings, bool, error) {
	event, err := ladoPhoneAuditActor(event, ladoPhoneAPIActor)
	if err != nil {
		return domain.LadoPhoneSettings{}, false, err
	}
	if err := s.writeMu.LockContext(ctx); err != nil {
		return domain.LadoPhoneSettings{}, false, err
	}
	defer s.writeMu.Unlock()
	var (
		stored  domain.LadoPhoneSettings
		changed bool
	)
	err = s.inTx(ctx, "disable LADO phone approval", func(q *gen.Queries) error {
		n, err := q.DisableLadoPhoneSettings(ctx, event.At.UTC())
		if err != nil {
			return err
		}
		if changed = n > 0; changed {
			if _, err := appendLadoAuditEventTx(ctx, q, event); err != nil {
				return err
			}
		}
		stored, err = ladoPhoneSettingsTx(ctx, q)
		return err
	})
	if err != nil {
		return domain.LadoPhoneSettings{}, false, err
	}
	return stored, changed, nil
}

// InsertLadoPhonePasskey enrolls a passkey in one transaction with the
// proof that allowed it and its audit event; the passkey's enrollment proof
// is the proof's nonce, and its time the event's when it has none. Only the
// desktop app's proof of domain.LadoPresencePhonePasskeyEnroll enrolls
// (domain.ErrLadoPhoneProofRefused otherwise), once
// (domain.ErrLadoPresenceReplayed), while phone approval is on for the
// passkey's relying party (domain.ErrLadoPhoneApprovalOff otherwise). A
// credential or fingerprint enrolled already, revoked or not, is refused
// with domain.ErrLadoPhonePasskeyExists. The event's actor is the proof's,
// as for EnableLadoPhone. When any part fails, nothing is kept.
func (s *Store) InsertLadoPhonePasskey(ctx context.Context, passkey domain.LadoPhonePasskey, proof domain.LadoPresenceProof, event domain.LadoAuditEvent) error {
	if err := ladoPhoneDesktopProof(proof, domain.LadoPresencePhonePasskeyEnroll); err != nil {
		return err
	}
	event, err := ladoPhoneAuditActor(event, proof.Actor())
	if err != nil {
		return err
	}
	created := passkey.CreatedAt
	if created.IsZero() {
		created = event.At
	}
	if err := s.writeMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.writeMu.Unlock()
	err = s.inTx(ctx, "enroll LADO phone passkey", func(q *gen.Queries) error {
		settings, err := ladoPhoneSettingsTx(ctx, q)
		if err != nil {
			return err
		}
		if !settings.Enabled || settings.RPID != passkey.RPID {
			return domain.ErrLadoPhoneApprovalOff
		}
		if err := insertLadoPresenceProof(ctx, q, proof); err != nil {
			return err
		}
		err = q.InsertLadoPhonePasskey(ctx, gen.InsertLadoPhonePasskeyParams{
			ID:               passkey.ID,
			CredentialID:     passkey.CredentialID,
			CredentialJson:   passkey.CredentialJSON,
			RpID:             passkey.RPID,
			DeviceHint:       passkey.DeviceHint,
			Login:            passkey.Login,
			Fingerprint:      passkey.Fingerprint,
			SignCount:        int64(passkey.SignCount),
			CreatedAt:        created.UTC(),
			EnrollProofNonce: proof.Nonce,
		})
		if isSQLiteUnique(err) || isSQLitePrimaryKey(err) {
			return domain.ErrLadoPhonePasskeyExists
		}
		if err != nil {
			return err
		}
		_, err = appendLadoAuditEventTx(ctx, q, event)
		return err
	})
	return unwrapLadoPhoneSentinel(err)
}

// LadoPhonePasskeys returns every enrolled passkey, revoked ones too, oldest
// first.
func (s *Store) LadoPhonePasskeys(ctx context.Context) ([]domain.LadoPhonePasskey, error) {
	rows, err := s.qr.ListLadoPhonePasskeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("read LADO phone passkeys: %w", err)
	}
	passkeys := make([]domain.LadoPhonePasskey, 0, len(rows))
	for _, row := range rows {
		passkeys = append(passkeys, ladoPhonePasskeyFromGen(row))
	}
	return passkeys, nil
}

// LadoPhonePasskeyByCredentialID returns the passkey of a WebAuthn
// credential id, revoked or not, and false when none is enrolled.
func (s *Store) LadoPhonePasskeyByCredentialID(ctx context.Context, credentialID []byte) (domain.LadoPhonePasskey, bool, error) {
	row, err := s.qr.GetLadoPhonePasskeyByCredentialID(ctx, credentialID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.LadoPhonePasskey{}, false, nil
	}
	if err != nil {
		return domain.LadoPhonePasskey{}, false, fmt.Errorf("read LADO phone passkey: %w", err)
	}
	return ladoPhonePasskeyFromGen(row), true, nil
}

// RecordLadoPhonePasskeyUse stores a passkey's sign count and last use after
// an assertion. A clone warning, once recorded, stays. A passkey that is not
// enrolled or was revoked returns domain.ErrLadoPhonePasskeyNotFound.
func (s *Store) RecordLadoPhonePasskeyUse(ctx context.Context, id string, signCount uint32, cloneWarning bool, at time.Time) error {
	if err := s.writeMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.writeMu.Unlock()
	n, err := s.qw.RecordLadoPhonePasskeyUse(ctx, gen.RecordLadoPhonePasskeyUseParams{
		SignCount:    int64(signCount),
		CloneWarning: ladoBool(cloneWarning),
		LastUsedAt:   ladoNullTime(at),
		ID:           id,
	})
	if err != nil {
		return fmt.Errorf("record LADO phone passkey use: %w", err)
	}
	if n == 0 {
		return domain.ErrLadoPhonePasskeyNotFound
	}
	return nil
}

// RevokeLadoPhonePasskey revokes a passkey at the event's time, with its
// audit event by "api", and reports whether this revoked it: revoking a
// revoked passkey changes and records nothing. An id no passkey has returns
// domain.ErrLadoPhonePasskeyNotFound.
func (s *Store) RevokeLadoPhonePasskey(ctx context.Context, id string, event domain.LadoAuditEvent) (bool, error) {
	event, err := ladoPhoneAuditActor(event, ladoPhoneAPIActor)
	if err != nil {
		return false, err
	}
	if err := s.writeMu.LockContext(ctx); err != nil {
		return false, err
	}
	defer s.writeMu.Unlock()
	var revoked bool
	err = s.inTx(ctx, "revoke LADO phone passkey", func(q *gen.Queries) error {
		n, err := q.RevokeLadoPhonePasskey(ctx, gen.RevokeLadoPhonePasskeyParams{RevokedAt: ladoNullTime(event.At), ID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			count, err := q.CountLadoPhonePasskeysByID(ctx, id)
			if err != nil {
				return err
			}
			if count == 0 {
				return domain.ErrLadoPhonePasskeyNotFound
			}
			return nil
		}
		revoked = true
		_, err = appendLadoAuditEventTx(ctx, q, event)
		return err
	})
	if err != nil {
		return false, unwrapLadoPhoneSentinel(err)
	}
	return revoked, nil
}

// ladoPhoneDesktopProof refuses a proof that is not the desktop app's proof
// of action: a development proof, one a passkey made, or one for another
// action.
func ladoPhoneDesktopProof(proof domain.LadoPresenceProof, action string) error {
	if proof.Action != action || proof.Dev || proof.Via != "" {
		return domain.ErrLadoPhoneProofRefused
	}
	return nil
}

// ladoPhoneAuditActor returns the event with actor as its actor, and refuses
// an event that names another.
func ladoPhoneAuditActor(event domain.LadoAuditEvent, actor string) (domain.LadoAuditEvent, error) {
	if event.Actor != "" && event.Actor != actor {
		return domain.LadoAuditEvent{}, fmt.Errorf("LADO phone approval audit: actor %q, want %q", event.Actor, actor)
	}
	event.Actor = actor
	return event, nil
}

// ladoPhoneSettingsTx reads the settings row, the zero value without one.
func ladoPhoneSettingsTx(ctx context.Context, q *gen.Queries) (domain.LadoPhoneSettings, error) {
	row, err := q.GetLadoPhoneSettings(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.LadoPhoneSettings{}, nil
	}
	if err != nil {
		return domain.LadoPhoneSettings{}, err
	}
	return domain.LadoPhoneSettings{
		Enabled:          row.Enabled == 1,
		RPID:             row.RpID,
		Origin:           row.Origin,
		OwnerLogin:       row.OwnerLogin,
		UserHandle:       row.UserHandle,
		ServePort:        int(row.ServePort),
		SocketPath:       row.SocketPath,
		EnableProofNonce: row.EnableProofNonce.String,
		UpdatedAt:        row.UpdatedAt.UTC(),
	}, nil
}

func ladoPhonePasskeyFromGen(row gen.LadoPhonePasskey) domain.LadoPhonePasskey {
	return domain.LadoPhonePasskey{
		ID:               row.ID,
		CredentialID:     row.CredentialID,
		CredentialJSON:   row.CredentialJson,
		RPID:             row.RpID,
		DeviceHint:       row.DeviceHint,
		Login:            row.Login,
		Fingerprint:      row.Fingerprint,
		SignCount:        ladoPhoneSignCount(row.SignCount),
		CloneWarning:     row.CloneWarning == 1,
		CreatedAt:        row.CreatedAt.UTC(),
		LastUsedAt:       ladoTimeFromNull(row.LastUsedAt),
		RevokedAt:        ladoTimeFromNull(row.RevokedAt),
		EnrollProofNonce: row.EnrollProofNonce,
	}
}

// ladoPhoneSignCount is a stored sign count as WebAuthn counts it. The table
// refuses a negative count and the store writes uint32 values only; a larger
// one reads as the largest, so the next assertion's count looks like a
// regression (a clone warning) rather than a fresh start.
func ladoPhoneSignCount(n int64) uint32 {
	switch {
	case n <= 0:
		return 0
	case n > math.MaxUint32:
		return math.MaxUint32
	}
	return uint32(n)
}

// unwrapLadoPhoneSentinel returns a phone approval sentinel as is, so
// callers can match it without the transaction's wrapping text.
func unwrapLadoPhoneSentinel(err error) error {
	for _, sentinel := range []error{
		domain.ErrLadoPresenceReplayed,
		domain.ErrLadoPhoneApprovalOff,
		domain.ErrLadoPhonePasskeyExists,
		domain.ErrLadoPhonePasskeyNotFound,
	} {
		if errors.Is(err, sentinel) {
			return sentinel
		}
	}
	return err
}
