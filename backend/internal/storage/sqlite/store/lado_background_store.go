package store

// LADO fork (2026-11-27): a session's live background tasks
// (migrations_lado/0025, domain/lado_background.go).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// SetLadoSessionBackground records a session's live background tasks as the
// provider reported them at at. A nil list forgets the bridge's report: AO
// no longer hears from the bridge (a transcript reading stays).
// The same list again writes nothing, so it publishes nothing.
func (s *Store) SetLadoSessionBackground(ctx context.Context, id domain.SessionID, tasks []domain.BackgroundTask, at time.Time) error {
	if id == "" {
		return errors.New("set session background tasks: a session is required")
	}
	if err := s.writeMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.writeMu.Unlock()
	if tasks == nil {
		if err := s.qw.DeleteLadoSessionBackground(ctx, string(id)); err != nil {
			return fmt.Errorf("forget the background tasks of %s: %w", id, err)
		}
		return nil
	}
	data, err := json.Marshal(domain.NormalizeBackgroundTasks(tasks))
	if err != nil {
		return fmt.Errorf("encode the background tasks of %s: %w", id, err)
	}
	if err := s.qw.UpsertLadoSessionBackground(ctx, gen.UpsertLadoSessionBackgroundParams{
		SessionID: string(id), TasksJson: string(data), UpdatedAt: at.UTC(),
	}); err != nil {
		return fmt.Errorf("record the background tasks of %s: %w", id, err)
	}
	return nil
}

// SetLadoSessionBackgroundReading records what the LADO watch read of a
// worker's background tasks when its bridge cannot report them. A bridge
// report stays; the same reading again writes nothing.
func (s *Store) SetLadoSessionBackgroundReading(ctx context.Context, id domain.SessionID, tasks []domain.BackgroundTask, at time.Time) error {
	if id == "" {
		return errors.New("set session background reading: a session is required")
	}
	if tasks == nil {
		tasks = []domain.BackgroundTask{}
	}
	data, err := json.Marshal(domain.NormalizeBackgroundTasks(tasks))
	if err != nil {
		return fmt.Errorf("encode the background reading of %s: %w", id, err)
	}
	if err := s.writeMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.writeMu.Unlock()
	if err := s.qw.UpsertLadoSessionBackgroundReading(ctx, gen.UpsertLadoSessionBackgroundReadingParams{
		SessionID: string(id), TasksJson: string(data), UpdatedAt: at.UTC(),
	}); err != nil {
		return fmt.Errorf("record the background reading of %s: %w", id, err)
	}
	return nil
}

// ForgetLadoSessionBackgroundReading drops the transcript reading of a
// session whose conversation file can no longer be read.
func (s *Store) ForgetLadoSessionBackgroundReading(ctx context.Context, id domain.SessionID) error {
	if err := s.writeMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.writeMu.Unlock()
	if err := s.qw.DeleteLadoSessionBackgroundReading(ctx, string(id)); err != nil {
		return fmt.Errorf("forget the background reading of %s: %w", id, err)
	}
	return nil
}

// LadoSessionBackground is what AO last heard of a session's background
// tasks; ok is false when it does not know.
func (s *Store) LadoSessionBackground(ctx context.Context, id domain.SessionID) (domain.SessionBackgroundTasks, bool, error) {
	row, err := s.qr.GetLadoSessionBackground(ctx, string(id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SessionBackgroundTasks{}, false, nil
	}
	if err != nil {
		return domain.SessionBackgroundTasks{}, false, fmt.Errorf("read the background tasks of %s: %w", id, err)
	}
	b, err := ladoBackgroundFromRow(row.SessionID, row.TasksJson, row.Source, row.UpdatedAt)
	return b, err == nil, err
}

// ListLiveLadoSessionBackground lists the background tasks AO knows of every
// session that has not ended.
func (s *Store) ListLiveLadoSessionBackground(ctx context.Context) ([]domain.SessionBackgroundTasks, error) {
	rows, err := s.qr.ListLiveLadoSessionBackground(ctx)
	if err != nil {
		return nil, fmt.Errorf("list session background tasks: %w", err)
	}
	out := make([]domain.SessionBackgroundTasks, 0, len(rows))
	for _, row := range rows {
		b, err := ladoBackgroundFromRow(row.SessionID, row.TasksJson, row.Source, row.UpdatedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func ladoBackgroundFromRow(id, tasksJSON, source string, updated time.Time) (domain.SessionBackgroundTasks, error) {
	var tasks []domain.BackgroundTask
	if err := json.Unmarshal([]byte(tasksJSON), &tasks); err != nil {
		return domain.SessionBackgroundTasks{}, fmt.Errorf("decode the background tasks of %s: %w", id, err)
	}
	if tasks == nil {
		tasks = []domain.BackgroundTask{}
	}
	return domain.SessionBackgroundTasks{SessionID: domain.SessionID(id), Tasks: tasks, Source: source, UpdatedAt: updated.UTC()}, nil
}
