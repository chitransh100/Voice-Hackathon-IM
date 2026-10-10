package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vani-context/migrations"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound    = errors.New("context not found")
	ErrUnavailable = errors.New("storage unavailable")
)

type ContextSnapshot struct {
	GLUSRID             string          `json:"glusr_id"`
	Role                string          `json:"role"`
	ProfileJSON         json.RawMessage `json:"profile_json"`
	BusinessName        *string         `json:"business_name"`
	City                *string         `json:"city"`
	ProfileUpdatedAt    string          `json:"profile_updated_at"`
	Preferences         json.RawMessage `json:"preferences"`
	Version             int64           `json:"version"`
	ContextMD           string          `json:"context_md"`
	UpdatedAt           string          `json:"updated_at"`
	LatestSourceEventAt *string         `json:"latest_source_event_at"`
	TokenCount          *int64          `json:"token_count"`
	TokenCountMethod    string          `json:"token_count_method"`
	SourceCoverage      json.RawMessage `json:"source_coverage"`
}

type ContextRepository interface {
	GetPreparedContext(ctx context.Context, glusrID, role string) (ContextSnapshot, error)
	Ready(ctx context.Context) error
}

type SQLiteStore struct {
	db *sql.DB
}

func Open(ctx context.Context, path string, busyTimeout time.Duration) (*SQLiteStore, error) {
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// One connection makes connection-level SQLite pragmas deterministic and is
	// sufficient for the single-host MVP's one-writer design.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &SQLiteStore{db: db}
	if err := store.configure(ctx, busyTimeout); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) configure(ctx context.Context, busyTimeout time.Duration) error {
	statements := []string{
		"PRAGMA foreign_keys = ON",
		fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeout.Milliseconds()),
		"PRAGMA journal_mode = WAL",
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite: %w", err)
		}
	}
	return nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) Migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > 1 {
		return fmt.Errorf("database schema version %d is newer than supported version 1", version)
	}
	if version == 0 {
		if _, err := tx.ExecContext(ctx, migrations.InitialSQL); err != nil {
			return fmt.Errorf("apply migration 001: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
			return fmt.Errorf("record schema version: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

// GetPreparedContext intentionally executes exactly one current_context lookup.
// It performs no join, history scan, rendering, or external call.
func (s *SQLiteStore) GetPreparedContext(ctx context.Context, glusrID, role string) (ContextSnapshot, error) {
	const query = `SELECT glusr_id, role, memory_json, version, context_md, updated_at,
latest_source_event_at, token_count, token_count_method, source_coverage_json
FROM current_context WHERE glusr_id = ? AND role = ?`
	var snapshot ContextSnapshot
	var latest sql.NullString
	var tokens sql.NullInt64
	var memoryJSON, coverage string
	err := s.db.QueryRowContext(ctx, query, glusrID, role).Scan(
		&snapshot.GLUSRID, &snapshot.Role, &memoryJSON, &snapshot.Version, &snapshot.ContextMD,
		&snapshot.UpdatedAt, &latest, &tokens, &snapshot.TokenCountMethod, &coverage,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ContextSnapshot{}, ErrNotFound
	}
	if err != nil {
		if isBusy(err) {
			return ContextSnapshot{}, ErrUnavailable
		}
		return ContextSnapshot{}, fmt.Errorf("get prepared context: %w", err)
	}
	if latest.Valid {
		snapshot.LatestSourceEventAt = &latest.String
	}
	if tokens.Valid {
		snapshot.TokenCount = &tokens.Int64
	}
	if !json.Valid([]byte(coverage)) {
		return ContextSnapshot{}, fmt.Errorf("invalid source coverage in stored snapshot")
	}
	var memory struct {
		Profile struct {
			ProfileJSON      json.RawMessage `json:"profile_json"`
			BusinessName     *string         `json:"business_name"`
			City             *string         `json:"city"`
			ProfileUpdatedAt string          `json:"profile_updated_at"`
		} `json:"profile"`
		Preferences json.RawMessage `json:"preferences"`
	}
	if err := json.Unmarshal([]byte(memoryJSON), &memory); err != nil {
		return ContextSnapshot{}, fmt.Errorf("invalid memory in stored snapshot: %w", err)
	}
	if len(memory.Profile.ProfileJSON) == 0 || !json.Valid(memory.Profile.ProfileJSON) || memory.Profile.ProfileUpdatedAt == "" {
		return ContextSnapshot{}, fmt.Errorf("stored snapshot requires profile backfill")
	}
	if len(memory.Preferences) == 0 {
		memory.Preferences = json.RawMessage(`{}`)
	}
	if !json.Valid(memory.Preferences) {
		return ContextSnapshot{}, fmt.Errorf("invalid preferences in stored snapshot")
	}
	snapshot.ProfileJSON = memory.Profile.ProfileJSON
	snapshot.BusinessName = memory.Profile.BusinessName
	snapshot.City = memory.Profile.City
	snapshot.ProfileUpdatedAt = memory.Profile.ProfileUpdatedAt
	snapshot.Preferences = memory.Preferences
	snapshot.SourceCoverage = json.RawMessage(coverage)
	return snapshot, nil
}

func (s *SQLiteStore) Ready(ctx context.Context) error {
	var one int
	if err := s.db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("database readiness: %w", err)
	}
	return nil
}

func isBusy(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database is busy")
}
