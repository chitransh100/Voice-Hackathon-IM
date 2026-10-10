package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrUnavailable = errors.New("storage unavailable")

type Candidate struct {
	InteractionID       string
	OccurredAt          string
	Channel             string
	ThreadID            *string
	Summary             *string
	ImportantPointsJSON string
	SourcePayloadJSON   string
	Source              string
	SourceEventID       string
	SourceRevision      int
	ProcessedRevision   int
	ProcessingStatus    string
}

type Store struct {
	db *sql.DB
}

func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open shared sqlite database: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	for _, statement := range []string{"PRAGMA foreign_keys = ON", "PRAGMA busy_timeout = 5000", "PRAGMA query_only = ON"} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure shared sqlite database: %w", err)
		}
	}
	result := &Store{db: db}
	if err := result.Ready(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return result, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ready(ctx context.Context) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
WHERE type = 'table' AND name IN ('users', 'interactions', 'current_context')`).Scan(&count); err != nil {
		return classify("database readiness", err)
	}
	if count != 3 {
		return fmt.Errorf("database readiness: expected users, interactions and current_context tables")
	}
	return nil
}

func (s *Store) Candidates(ctx context.Context, glusrID, role, from, to string, limit int) ([]Candidate, bool, error) {
	const query = `SELECT interaction_id, occurred_at, channel, thread_id, summary,
important_points_json, source_payload_json, source, source_event_id,
source_revision, processed_revision, processing_status
FROM interactions
WHERE glusr_id = ? AND role = ?
  AND (? = '' OR occurred_at >= ?)
  AND (? = '' OR occurred_at < ?)
ORDER BY occurred_at DESC, interaction_id
LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, glusrID, role, from, from, to, to, limit+1)
	if err != nil {
		return nil, false, classify("read history candidates", err)
	}
	defer rows.Close()
	items, err := scanCandidates(rows)
	if err != nil {
		return nil, false, err
	}
	limited := len(items) > limit
	if limited {
		items = items[:limit]
	}
	return items, limited, nil
}

func (s *Store) LaterCandidates(ctx context.Context, glusrID, role, after, to string, limit int) ([]Candidate, bool, error) {
	const query = `SELECT interaction_id, occurred_at, channel, thread_id, summary,
important_points_json, source_payload_json, source, source_event_id,
source_revision, processed_revision, processing_status
FROM interactions
WHERE glusr_id = ? AND role = ? AND occurred_at > ?
  AND (? = '' OR occurred_at < ?)
ORDER BY occurred_at ASC, interaction_id
LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, glusrID, role, after, to, to, limit+1)
	if err != nil {
		return nil, false, classify("read related updates", err)
	}
	defer rows.Close()
	items, err := scanCandidates(rows)
	if err != nil {
		return nil, false, err
	}
	limited := len(items) > limit
	if limited {
		items = items[:limit]
	}
	return items, limited, nil
}

func (s *Store) ThreadOwned(ctx context.Context, glusrID, role, threadID string) (bool, error) {
	const query = `SELECT thread_id, important_points_json FROM interactions
WHERE glusr_id = ? AND role = ? AND (thread_id = ? OR important_points_json LIKE ? ESCAPE '\')
LIMIT 501`
	// LIKE only bounds the candidate read. Exact ownership is established after
	// JSON parsing by the search layer; the identity values remain parameterized.
	rows, err := s.db.QueryContext(ctx, query, glusrID, role, threadID, "%"+escapeLike(threadID)+"%")
	if err != nil {
		return false, classify("verify thread ownership", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rowThread sql.NullString
		var points string
		if err := rows.Scan(&rowThread, &points); err != nil {
			return false, classify("scan thread ownership", err)
		}
		if rowThread.Valid && rowThread.String == threadID {
			return true, nil
		}
		if exactThreadInPoints(points, threadID) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func scanCandidates(rows *sql.Rows) ([]Candidate, error) {
	var items []Candidate
	for rows.Next() {
		var item Candidate
		var thread, summary sql.NullString
		if err := rows.Scan(&item.InteractionID, &item.OccurredAt, &item.Channel, &thread, &summary,
			&item.ImportantPointsJSON, &item.SourcePayloadJSON, &item.Source, &item.SourceEventID,
			&item.SourceRevision, &item.ProcessedRevision, &item.ProcessingStatus); err != nil {
			return nil, classify("scan history candidate", err)
		}
		if thread.Valid {
			item.ThreadID = &thread.String
		}
		if summary.Valid {
			item.Summary = &summary.String
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, classify("iterate history candidates", err)
	}
	return items, nil
}

func exactThreadInPoints(raw, threadID string) bool {
	needle := `"thread_id":"` + strings.ReplaceAll(threadID, `"`, `\"`) + `"`
	return strings.Contains(strings.ReplaceAll(raw, " ", ""), needle)
}

func escapeLike(value string) string {
	return strings.NewReplacer("%", "\\%", "_", "\\_").Replace(value)
}

func classify(operation string, err error) error {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "database is locked") || strings.Contains(message, "database is busy") || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, ErrUnavailable)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func FormatUTC(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}
