package supervisornotice

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"github.com/charmbracelet/crush/internal/db"
)

type Notice struct {
	ID                   string
	SessionID            string
	Source               string
	Status               string
	Title                string
	Body                 string
	CorrectivePrompt     string
	SelectedModel        string
	ActiveExecutionModel string
	ReasonCode           string
	CreatedAt            int64
	UpdatedAt            int64
	ShownAt              int64
	AcknowledgedAt       int64
}

type Store struct {
	db *sql.DB
}

func NewStore(ctx context.Context, dataDir string) (*Store, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("data directory is not set")
	}
	dbPath := filepath.Join(dataDir, "crush.db")
	conn, err := db.OpenPath(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	store := &Store{db: conn}
	if err := store.EnsureSchema(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("notice store is not initialized")
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS supervisor_notices (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT 'stack-orchestrator',
			status TEXT NOT NULL DEFAULT 'pending',
			title TEXT NOT NULL,
			body TEXT NOT NULL,
			corrective_prompt TEXT NOT NULL DEFAULT '',
			selected_model TEXT NOT NULL DEFAULT '',
			active_execution_model TEXT NOT NULL DEFAULT '',
			reason_code TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			shown_at INTEGER,
			acknowledged_at INTEGER
		)
	`); err != nil {
		return fmt.Errorf("creating supervisor_notices table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_supervisor_notices_session_pending
		ON supervisor_notices(session_id, acknowledged_at, created_at DESC)
	`); err != nil {
		return fmt.Errorf("creating supervisor_notices index: %w", err)
	}
	return nil
}

func (s *Store) GetLatestPending(ctx context.Context, sessionID string) (*Notice, error) {
	if s == nil || s.db == nil || sessionID == "" {
		return nil, nil
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT
			id, session_id, source, status, title, body, corrective_prompt,
			selected_model, active_execution_model, reason_code,
			created_at, updated_at, COALESCE(shown_at, 0), COALESCE(acknowledged_at, 0)
		FROM supervisor_notices
		WHERE session_id = ? AND acknowledged_at IS NULL
		ORDER BY created_at DESC
		LIMIT 1
	`, sessionID)
	var notice Notice
	if err := row.Scan(
		&notice.ID,
		&notice.SessionID,
		&notice.Source,
		&notice.Status,
		&notice.Title,
		&notice.Body,
		&notice.CorrectivePrompt,
		&notice.SelectedModel,
		&notice.ActiveExecutionModel,
		&notice.ReasonCode,
		&notice.CreatedAt,
		&notice.UpdatedAt,
		&notice.ShownAt,
		&notice.AcknowledgedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &notice, nil
}

func (s *Store) MarkShown(ctx context.Context, id string) error {
	if s == nil || s.db == nil || id == "" {
		return nil
	}
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		UPDATE supervisor_notices
		SET
			status = CASE WHEN status = 'pending' THEN 'shown' ELSE status END,
			shown_at = CASE WHEN shown_at IS NULL THEN ? ELSE shown_at END,
			updated_at = ?
		WHERE id = ?
	`, now, now, id)
	return err
}

func (s *Store) AcknowledgeSession(ctx context.Context, sessionID string) error {
	if s == nil || s.db == nil || sessionID == "" {
		return nil
	}
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		UPDATE supervisor_notices
		SET
			status = 'acknowledged',
			acknowledged_at = CASE WHEN acknowledged_at IS NULL THEN ? ELSE acknowledged_at END,
			updated_at = ?
		WHERE session_id = ? AND acknowledged_at IS NULL
	`, now, now, sessionID)
	return err
}
