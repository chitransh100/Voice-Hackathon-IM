package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

type UserKey struct {
	GLUSRID string
	Role    string
}

type UserRecord struct {
	GLUSRID          string
	Role             string
	BusinessName     *string
	City             *string
	ProfileJSON      json.RawMessage
	ProfileUpdatedAt string
}

type InteractionRecord struct {
	InteractionID       string
	Channel             string
	Source              string
	SourceEventID       string
	SourceRevision      int
	OccurredAt          string
	UpdatedAt           string
	ThreadID            *string
	Summary             *string
	ImportantPointsJSON json.RawMessage
	SourcePayloadJSON   json.RawMessage
}

type ExistingSnapshot struct {
	MemoryJSON json.RawMessage
	Version    int64
}

type BuildInput struct {
	User                        UserRecord
	Interactions                []InteractionRecord
	Existing                    *ExistingSnapshot
	LatestInteractionOccurredAt *string
	LatestInteractionUpdatedAt  *string
}

type PublishedSnapshot struct {
	MemoryJSON             json.RawMessage
	ContextMD              string
	TokenCount             int64
	TokenCountMethod       string
	SourceCoverageJSON     json.RawMessage
	LatestSourceEventAt    *string
	ExpectedProfileUpdated string
	ExpectedInteractionMax *string
	UpdatedAt              string
	Version                int64
}

func (s *SQLiteStore) ListUserKeys(ctx context.Context) ([]UserKey, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT glusr_id, role FROM users ORDER BY glusr_id, role")
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var keys []UserKey
	for rows.Next() {
		var key UserKey
		if err := rows.Scan(&key.GLUSRID, &key.Role); err != nil {
			return nil, fmt.Errorf("scan user key: %w", err)
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *SQLiteStore) LoadBuildInput(ctx context.Context, glusrID, role string) (BuildInput, error) {
	var input BuildInput
	input.User.GLUSRID, input.User.Role = glusrID, role
	var businessName, city sql.NullString
	var profile string
	err := s.db.QueryRowContext(ctx, `SELECT business_name, city, profile_json, profile_updated_at
FROM users WHERE glusr_id = ? AND role = ?`, glusrID, role).Scan(
		&businessName, &city, &profile, &input.User.ProfileUpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return BuildInput{}, ErrNotFound
	}
	if err != nil {
		return BuildInput{}, fmt.Errorf("load user for rebuild: %w", err)
	}
	if !json.Valid([]byte(profile)) {
		return BuildInput{}, fmt.Errorf("stored profile_json is invalid")
	}
	input.User.ProfileJSON = json.RawMessage(profile)
	if businessName.Valid {
		input.User.BusinessName = &businessName.String
	}
	if city.Valid {
		input.User.City = &city.String
	}
	var existingMemory string
	var existingVersion int64
	err = s.db.QueryRowContext(ctx, `SELECT memory_json, version FROM current_context
WHERE glusr_id = ? AND role = ?`, glusrID, role).Scan(&existingMemory, &existingVersion)
	if err == nil {
		if !json.Valid([]byte(existingMemory)) {
			return BuildInput{}, fmt.Errorf("stored memory_json is invalid")
		}
		input.Existing = &ExistingSnapshot{MemoryJSON: json.RawMessage(existingMemory), Version: existingVersion}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return BuildInput{}, fmt.Errorf("load existing snapshot: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT interaction_id, channel, source, source_event_id,
source_revision, occurred_at, updated_at, thread_id, summary, important_points_json, source_payload_json
FROM interactions WHERE glusr_id = ? AND role = ?
ORDER BY occurred_at DESC, interaction_id`, glusrID, role)
	if err != nil {
		return BuildInput{}, fmt.Errorf("load interactions for rebuild: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item InteractionRecord
		var threadID, summary sql.NullString
		var points, sourcePayload string
		if err := rows.Scan(&item.InteractionID, &item.Channel, &item.Source, &item.SourceEventID,
			&item.SourceRevision, &item.OccurredAt, &item.UpdatedAt, &threadID, &summary, &points, &sourcePayload); err != nil {
			return BuildInput{}, fmt.Errorf("scan interaction for rebuild: %w", err)
		}
		if !json.Valid([]byte(points)) {
			return BuildInput{}, fmt.Errorf("interaction %s has invalid important_points_json", item.InteractionID)
		}
		if threadID.Valid {
			item.ThreadID = &threadID.String
		}
		if summary.Valid {
			item.Summary = &summary.String
		}
		item.ImportantPointsJSON = json.RawMessage(points)
		item.SourcePayloadJSON = json.RawMessage(sourcePayload)
		input.Interactions = append(input.Interactions, item)
		if input.LatestInteractionOccurredAt == nil || item.OccurredAt > *input.LatestInteractionOccurredAt {
			value := item.OccurredAt
			input.LatestInteractionOccurredAt = &value
		}
		if input.LatestInteractionUpdatedAt == nil || item.UpdatedAt > *input.LatestInteractionUpdatedAt {
			value := item.UpdatedAt
			input.LatestInteractionUpdatedAt = &value
		}
	}
	if err := rows.Err(); err != nil {
		return BuildInput{}, fmt.Errorf("iterate interactions for rebuild: %w", err)
	}
	return input, nil
}

func (s *SQLiteStore) NeedsRebuild(ctx context.Context, key UserKey) (bool, error) {
	input, err := s.LoadBuildInput(ctx, key.GLUSRID, key.Role)
	if err != nil {
		return false, err
	}
	if input.Existing == nil {
		return true, nil
	}
	var metadata struct {
		Profile struct {
			ProfileJSON      json.RawMessage `json:"profile_json"`
			BusinessName     *string         `json:"business_name"`
			City             *string         `json:"city"`
			ProfileUpdatedAt string          `json:"profile_updated_at"`
		} `json:"profile"`
		Build struct {
			InteractionsUpdatedAt *string `json:"interactions_updated_at"`
		} `json:"build"`
	}
	if err := json.Unmarshal(input.Existing.MemoryJSON, &metadata); err != nil {
		return true, nil
	}
	profilesEqual := jsonEqual(metadata.Profile.ProfileJSON, input.User.ProfileJSON) &&
		stringPointersEqual(metadata.Profile.BusinessName, input.User.BusinessName) &&
		stringPointersEqual(metadata.Profile.City, input.User.City) &&
		metadata.Profile.ProfileUpdatedAt == input.User.ProfileUpdatedAt
	return !profilesEqual || !stringPointersEqual(metadata.Build.InteractionsUpdatedAt, input.LatestInteractionUpdatedAt), nil
}

func (s *SQLiteStore) PublishSnapshot(ctx context.Context, key UserKey, snapshot PublishedSnapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin snapshot publication: %w", err)
	}
	defer tx.Rollback()
	var currentProfileUpdated string
	if err := tx.QueryRowContext(ctx, `SELECT profile_updated_at FROM users
WHERE glusr_id = ? AND role = ?`, key.GLUSRID, key.Role).Scan(&currentProfileUpdated); err != nil {
		return fmt.Errorf("recheck profile revision: %w", err)
	}
	if currentProfileUpdated != snapshot.ExpectedProfileUpdated {
		return errors.New("profile changed during snapshot rebuild")
	}
	var currentInteractionMax sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT MAX(updated_at) FROM interactions
WHERE glusr_id = ? AND role = ?`, key.GLUSRID, key.Role).Scan(&currentInteractionMax); err != nil {
		return fmt.Errorf("recheck interaction revision: %w", err)
	}
	var actualMax *string
	if currentInteractionMax.Valid {
		actualMax = &currentInteractionMax.String
	}
	if !stringPointersEqual(actualMax, snapshot.ExpectedInteractionMax) {
		return errors.New("interactions changed during snapshot rebuild")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO current_context
(glusr_id, role, memory_json, context_md, version, updated_at, latest_source_event_at,
 token_count, token_count_method, source_coverage_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(glusr_id, role) DO UPDATE SET
memory_json=excluded.memory_json, context_md=excluded.context_md, version=excluded.version,
updated_at=excluded.updated_at, latest_source_event_at=excluded.latest_source_event_at,
token_count=excluded.token_count, token_count_method=excluded.token_count_method,
source_coverage_json=excluded.source_coverage_json`, key.GLUSRID, key.Role,
		string(snapshot.MemoryJSON), snapshot.ContextMD, snapshot.Version, snapshot.UpdatedAt,
		snapshot.LatestSourceEventAt, snapshot.TokenCount, snapshot.TokenCountMethod,
		string(snapshot.SourceCoverageJSON))
	if err != nil {
		return fmt.Errorf("publish snapshot: %w", err)
	}
	return tx.Commit()
}

func (s *SQLiteStore) UpsertUser(ctx context.Context, user UserRecord) error {
	if user.Role != "buyer" && user.Role != "seller" {
		return errors.New("invalid role")
	}
	if !json.Valid(user.ProfileJSON) {
		return errors.New("invalid profile_json")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO users
(glusr_id, role, business_name, city, profile_json, profile_updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(glusr_id, role) DO UPDATE SET business_name=excluded.business_name,
city=excluded.city, profile_json=excluded.profile_json,
profile_updated_at=excluded.profile_updated_at`, user.GLUSRID, user.Role,
		user.BusinessName, user.City, string(user.ProfileJSON), user.ProfileUpdatedAt)
	return err
}

func (s *SQLiteStore) UpsertInteraction(ctx context.Context, glusrID, role string, item InteractionRecord) error {
	if !json.Valid(item.ImportantPointsJSON) {
		return errors.New("invalid important_points_json")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO interactions
(interaction_id, glusr_id, role, channel, source, source_event_id, source_revision,
 occurred_at, received_at, updated_at, session_state, thread_id, summary,
 important_points_json, source_payload_json, processing_status, processed_revision)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'completed', ?, ?, ?, '{}', 'processed', ?)
ON CONFLICT(interaction_id) DO UPDATE SET source_revision=excluded.source_revision,
occurred_at=excluded.occurred_at, updated_at=excluded.updated_at,
thread_id=excluded.thread_id, summary=excluded.summary,
important_points_json=excluded.important_points_json,
processing_status='processed', processed_revision=excluded.source_revision`,
		item.InteractionID, glusrID, role, item.Channel, item.Source, item.SourceEventID,
		item.SourceRevision, item.OccurredAt, item.UpdatedAt, item.UpdatedAt, item.ThreadID,
		item.Summary, string(item.ImportantPointsJSON), item.SourceRevision)
	return err
}

func jsonEqual(left, right json.RawMessage) bool {
	var a, b any
	if json.Unmarshal(left, &a) != nil || json.Unmarshal(right, &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

func stringPointersEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
