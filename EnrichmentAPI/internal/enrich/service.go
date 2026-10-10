package enrich

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"vani-context/enrichmentapi/shared"
	contextstorage "vani-context/internal/storage"
)

type Rebuilder interface {
	Rebuild(context.Context, contextstorage.UserKey) error
}

type Service struct {
	db      *sql.DB
	builder Rebuilder
	now     func() time.Time
}

type ServiceError struct {
	Status  int
	Code    string
	Message string
}

func (e *ServiceError) Error() string { return e.Message }

type existingInteraction struct {
	InteractionID     string
	Channel           string
	OccurredAt        string
	SessionState      string
	ThreadID          *string
	SourceRevision    int
	ProcessedRevision int
	ProcessingStatus  string
	SourcePayloadJSON string
}

type sourcePayload struct {
	Transcript      string               `json:"transcript_text"`
	TranscriptRaw   json.RawMessage      `json:"transcript_raw,omitempty"`
	TranscriptTurns []TranscriptTurn     `json:"transcript_turns,omitempty"`
	EnrichmentJSON  string               `json:"enrichment_json"`
	EnrichmentRaw   json.RawMessage      `json:"enrichment_raw,omitempty"`
	CanonicalHash   string               `json:"canonical_hash"`
	Validation      validationMetadata   `json:"validation"`
	ProfileChanges  []StoredChange       `json:"profile_changes"`
	RevisionHistory []revisionHistory    `json:"revision_history,omitempty"`
	EventTime       EventTimeMetadata    `json:"event_time"`
	InputAdapter    InputAdapterMetadata `json:"input_adapter"`
}

type validationMetadata struct {
	Contract            string              `json:"contract"`
	EvidenceChecked     bool                `json:"evidence_checked"`
	ValidatedAt         string              `json:"validated_at"`
	SummaryProvenance   string              `json:"summary_provenance"`
	TrustedPointCount   int                 `json:"trusted_point_count"`
	SubmittedPointCount int                 `json:"submitted_point_count"`
	Warnings            []ValidationWarning `json:"warnings"`
	Status              string              `json:"status"`
	Errors              []ValidationError   `json:"errors"`
}

func (s *Service) AcceptRaw(ctx context.Context, prepared Prepared) (Prepared, *Response, int, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return prepared, nil, 0, unavailable(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return prepared, nil, 0, unavailable(err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	var exists int
	err = conn.QueryRowContext(ctx, `SELECT 1 FROM users WHERE glusr_id=? AND role=?`, prepared.Request.GLUSRID, prepared.Request.Role).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return prepared, nil, 0, &ServiceError{Status: 404, Code: "user_not_found", Message: "no user exists for this GLID and role"}
	}
	if err != nil {
		return prepared, nil, 0, unavailable(err)
	}
	existing, err := loadExisting(ctx, conn, prepared)
	if err != nil {
		return prepared, nil, 0, err
	}
	if existing != nil {
		var prior sourcePayload
		if json.Unmarshal([]byte(existing.SourcePayloadJSON), &prior) != nil {
			return prepared, nil, 0, &ServiceError{Status: 503, Code: "invalid_stored_provenance", Message: "stored interaction provenance is invalid"}
		}
		if prior.CanonicalHash == prepared.CanonicalHash {
			prepared.InteractionID, prepared.AcceptedRevision, prepared.DuplicateDelivery = existing.InteractionID, existing.SourceRevision, true
			if prior.Validation.Status == "rejected" || (prior.Validation.Status == "accepted" && existing.ProcessingStatus == "processed") {
				response := responseFromStored(existing, prior.ProfileChanges, prior.Validation.Warnings, true)
				response.ValidationErrors = prior.Validation.Errors
				response.ExtractionStatus = prior.Validation.Status
				if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
					return prepared, nil, 0, unavailable(err)
				}
				committed = true
				status := 200
				if prior.Validation.Status == "rejected" || existing.ProcessingStatus != "processed" {
					status = 202
				}
				return prepared, &response, status, nil
			}
			if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
				return prepared, nil, 0, unavailable(err)
			}
			committed = true
			return prepared, nil, 0, nil
		}
		if existing.Channel != prepared.Request.Channel || existing.OccurredAt != prepared.NormalizedTime || !sameStringPointer(existing.ThreadID, prepared.NormalizedThread) {
			return prepared, nil, 0, &ServiceError{Status: 409, Code: "immutable_call_metadata_changed", Message: "changed delivery cannot alter channel, occurred_at, or thread_id"}
		}
		if existing.SessionState == "completed" && prepared.Request.SessionState == "ongoing" {
			return prepared, nil, 0, &ServiceError{Status: 409, Code: "stale_session_state", Message: "a completed interaction cannot be reopened"}
		}
	}
	revision, interactionIDValue := 1, interactionID(prepared)
	history := []revisionHistory{}
	if existing != nil {
		revision, interactionIDValue = existing.SourceRevision+1, existing.InteractionID
		var previous sourcePayload
		_ = json.Unmarshal([]byte(existing.SourcePayloadJSON), &previous)
		history = append(history, previous.RevisionHistory...)
		history = append(history, revisionHistory{Revision: existing.SourceRevision, CanonicalHash: previous.CanonicalHash,
			Transcript: previous.Transcript, EnrichmentJSON: previous.EnrichmentJSON, ProfileChanges: previous.ProfileChanges, Validation: previous.Validation})
	}
	now := shared.FormatUTC(s.now().UTC())
	payload := sourcePayload{Transcript: prepared.Request.Transcript, TranscriptRaw: append(json.RawMessage(nil), prepared.Request.TranscriptRaw...),
		TranscriptTurns: prepared.Request.TranscriptTurns, EnrichmentJSON: prepared.Request.EnrichmentJSON,
		EnrichmentRaw: append(json.RawMessage(nil), prepared.Request.EnrichmentRaw...), CanonicalHash: prepared.CanonicalHash,
		ProfileChanges: []StoredChange{}, RevisionHistory: history, EventTime: prepared.EventTime,
		InputAdapter: InputAdapterMetadata{OriginalSource: prepared.OriginalSource, CanonicalSource: prepared.Request.Source,
			OriginalChannel: prepared.OriginalChannel, CanonicalChannel: prepared.Request.Channel},
		Validation: validationMetadata{Contract: "memory_contract_v1", Status: "pending", EvidenceChecked: false,
			ValidatedAt: "", Warnings: []ValidationWarning{}, Errors: []ValidationError{}}}
	payloadJSON, _ := json.Marshal(payload)
	if existing == nil {
		_, err = conn.ExecContext(ctx, `INSERT INTO interactions(interaction_id,glusr_id,role,channel,source,source_event_id,
source_revision,occurred_at,received_at,updated_at,session_state,thread_id,summary,important_points_json,
source_payload_json,processing_status,processed_revision,retry_count,next_retry_at,last_error)
VALUES(?,?,?,?,?,?,1,?,?,?,?,?,NULL,'[]',?,'pending',0,0,NULL,NULL)`, interactionIDValue,
			prepared.Request.GLUSRID, prepared.Request.Role, prepared.Request.Channel, prepared.Request.Source,
			prepared.Request.SourceEventID, prepared.NormalizedTime, now, now, prepared.Request.SessionState,
			prepared.NormalizedThread, string(payloadJSON))
	} else {
		_, err = conn.ExecContext(ctx, `UPDATE interactions SET source_revision=?,received_at=?,updated_at=?,session_state=?,
source_payload_json=?,processing_status='pending',next_retry_at=NULL,last_error=NULL WHERE interaction_id=?`,
			revision, now, now, prepared.Request.SessionState, string(payloadJSON), interactionIDValue)
	}
	if err != nil {
		return prepared, nil, 0, unavailable(err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return prepared, nil, 0, unavailable(err)
	}
	committed = true
	prepared.InteractionID, prepared.AcceptedRevision = interactionIDValue, revision
	return prepared, nil, 0, nil
}

func (s *Service) RejectExtraction(ctx context.Context, prepared Prepared, code, message string) (Response, int, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT source_payload_json FROM interactions WHERE interaction_id=? AND source_revision=?`, prepared.InteractionID, prepared.AcceptedRevision).Scan(&raw); err != nil {
		return Response{}, 0, unavailable(err)
	}
	var payload sourcePayload
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return Response{}, 0, &ServiceError{Status: 503, Code: "invalid_stored_provenance", Message: "stored interaction provenance is invalid"}
	}
	now := shared.FormatUTC(s.now().UTC())
	payload.Validation.Status = "rejected"
	payload.Validation.EvidenceChecked = false
	payload.Validation.ValidatedAt = now
	payload.Validation.SummaryProvenance = "model_generated_unvalidated"
	payload.Validation.Errors = []ValidationError{{Code: code, Message: message}}
	payloadJSON, _ := json.Marshal(payload)
	result, err := s.db.ExecContext(ctx, `UPDATE interactions SET source_payload_json=?,processing_status='failed',
last_error=?,next_retry_at=NULL,updated_at=? WHERE interaction_id=? AND source_revision=?`, string(payloadJSON), code, now,
		prepared.InteractionID, prepared.AcceptedRevision)
	if err != nil {
		return Response{}, 0, unavailable(err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return Response{}, 0, &ServiceError{Status: 409, Code: "revision_changed", Message: "interaction revision changed during validation"}
	}
	return Response{InteractionID: prepared.InteractionID, AcceptedRevision: prepared.AcceptedRevision,
		ProcessingStatus: "failed", Duplicate: prepared.DuplicateDelivery, AppliedChanges: []ChangeResult{},
		SkippedChanges: []ChangeResult{}, ConflictingChanges: []ChangeResult{}, ValidationWarnings: []ValidationWarning{},
		ExtractionStatus: "rejected", ValidationErrors: payload.Validation.Errors}, 202, nil
}

type revisionHistory struct {
	Revision       int                `json:"revision"`
	CanonicalHash  string             `json:"canonical_hash"`
	Transcript     string             `json:"transcript_text"`
	EnrichmentJSON string             `json:"enrichment_json"`
	ProfileChanges []StoredChange     `json:"profile_changes"`
	Validation     validationMetadata `json:"validation"`
}

func NewService(db *sql.DB, builder Rebuilder, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{db: db, builder: builder, now: now}
}

func (s *Service) Process(ctx context.Context, prepared Prepared) (Response, int, error) {
	result, rebuild, err := s.persist(ctx, prepared)
	if err != nil {
		return Response{}, 0, err
	}
	if !rebuild {
		return result, 200, nil
	}
	if err := s.builder.Rebuild(ctx, contextstorage.UserKey{GLUSRID: prepared.Request.GLUSRID, Role: prepared.Request.Role}); err != nil {
		s.markBuildFailed(context.Background(), result.InteractionID, result.AcceptedRevision)
		result.ProcessingStatus = "failed"
		return result, 202, nil
	}
	version, err := s.markProcessed(ctx, result.InteractionID, result.AcceptedRevision, prepared.Request.GLUSRID, prepared.Request.Role)
	if err != nil {
		result.ProcessingStatus = "pending"
		return result, 202, nil
	}
	result.ProcessingStatus = "processed"
	result.ContextVersion = &version
	return result, 200, nil
}

func (s *Service) persist(ctx context.Context, prepared Prepared) (Response, bool, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Response{}, false, unavailable(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return Response{}, false, unavailable(err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var businessName, city sql.NullString
	var profileRaw, profileUpdatedAt string
	err = conn.QueryRowContext(ctx, `SELECT business_name, city, profile_json, profile_updated_at
FROM users WHERE glusr_id = ? AND role = ?`, prepared.Request.GLUSRID, prepared.Request.Role).
		Scan(&businessName, &city, &profileRaw, &profileUpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Response{}, false, &ServiceError{Status: 404, Code: "user_not_found", Message: "no user exists for this GLID and role"}
	}
	if err != nil {
		return Response{}, false, unavailable(err)
	}
	profile, err := decodeObject([]byte(profileRaw))
	if err != nil {
		return Response{}, false, &ServiceError{Status: 503, Code: "invalid_stored_profile", Message: "stored profile cannot be enriched safely"}
	}

	existing, err := loadExisting(ctx, conn, prepared)
	if err != nil {
		return Response{}, false, err
	}
	if existing != nil {
		var payload sourcePayload
		if json.Unmarshal([]byte(existing.SourcePayloadJSON), &payload) != nil {
			return Response{}, false, &ServiceError{Status: 503, Code: "invalid_stored_provenance", Message: "stored interaction provenance is invalid"}
		}
		if payload.CanonicalHash == prepared.CanonicalHash && !(prepared.AcceptedRevision == existing.SourceRevision && payload.Validation.Status == "pending") {
			response := responseFromStored(existing, payload.ProfileChanges, payload.Validation.Warnings, true)
			if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
				return Response{}, false, unavailable(err)
			}
			committed = true
			return response, existing.ProcessingStatus != "processed" || existing.ProcessedRevision < existing.SourceRevision, nil
		}
		if existing.Channel != prepared.Request.Channel || existing.OccurredAt != prepared.NormalizedTime || !sameStringPointer(existing.ThreadID, prepared.NormalizedThread) {
			return Response{}, false, &ServiceError{Status: 409, Code: "immutable_call_metadata_changed", Message: "changed delivery cannot alter channel, occurred_at, or thread_id"}
		}
		if existing.SessionState == "completed" && prepared.Request.SessionState == "ongoing" {
			return Response{}, false, &ServiceError{Status: 409, Code: "stale_session_state", Message: "a completed interaction cannot be reopened"}
		}
	}

	revision := 1
	interactionIDValue := interactionID(prepared)
	var history []revisionHistory
	if existing != nil && prepared.AcceptedRevision == existing.SourceRevision {
		revision = existing.SourceRevision
		interactionIDValue = existing.InteractionID
		var pending sourcePayload
		_ = json.Unmarshal([]byte(existing.SourcePayloadJSON), &pending)
		history = append(history, pending.RevisionHistory...)
	} else if existing != nil {
		revision = existing.SourceRevision + 1
		interactionIDValue = existing.InteractionID
		var previous sourcePayload
		_ = json.Unmarshal([]byte(existing.SourcePayloadJSON), &previous)
		history = append(history, previous.RevisionHistory...)
		history = append(history, revisionHistory{Revision: existing.SourceRevision, CanonicalHash: previous.CanonicalHash,
			Transcript: previous.Transcript, EnrichmentJSON: previous.EnrichmentJSON, ProfileChanges: previous.ProfileChanges,
			Validation: previous.Validation})
	}

	latest, err := loadLatestProfileChanges(ctx, conn, prepared.Request.GLUSRID, prepared.Request.Role, interactionIDValue)
	if err != nil {
		return Response{}, false, unavailable(err)
	}
	prepared, err = resolveThreads(ctx, conn, prepared, interactionIDValue)
	if err != nil {
		return Response{}, false, unavailable(err)
	}
	storedChanges, applied, skipped, conflicting, profileChanged := applyUpdates(profile, prepared, revision, latest)
	now := shared.FormatUTC(s.now().UTC())
	if profileChanged {
		encodedProfile, err := json.Marshal(profile)
		if err != nil {
			return Response{}, false, &ServiceError{Status: 503, Code: "profile_encoding_failed", Message: "profile update could not be encoded"}
		}
		profileResult, err := conn.ExecContext(ctx, `UPDATE users SET profile_json = ?, profile_updated_at = ?
WHERE glusr_id = ? AND role = ? AND profile_updated_at = ?`, string(encodedProfile), now,
			prepared.Request.GLUSRID, prepared.Request.Role, profileUpdatedAt)
		if err != nil {
			return Response{}, false, unavailable(err)
		}
		if rows, err := profileResult.RowsAffected(); err != nil || rows != 1 {
			return Response{}, false, &ServiceError{Status: 409, Code: "concurrent_profile_update",
				Message: "profile changed while enrichment was being applied; retry this delivery as a revision"}
		}
	}
	payload := sourcePayload{Transcript: prepared.Request.Transcript, TranscriptRaw: append(json.RawMessage(nil), prepared.Request.TranscriptRaw...),
		TranscriptTurns: prepared.Request.TranscriptTurns, EnrichmentJSON: prepared.Request.EnrichmentJSON,
		EnrichmentRaw: append(json.RawMessage(nil), prepared.Request.EnrichmentRaw...),
		CanonicalHash: prepared.CanonicalHash, ProfileChanges: storedChanges, RevisionHistory: history,
		EventTime: prepared.EventTime,
		InputAdapter: InputAdapterMetadata{OriginalSource: prepared.OriginalSource, CanonicalSource: prepared.Request.Source,
			OriginalChannel: prepared.OriginalChannel, CanonicalChannel: prepared.Request.Channel},
		Validation: validationMetadata{Contract: "memory_contract_v1", Status: "accepted", EvidenceChecked: true, ValidatedAt: now,
			SummaryProvenance: "model_generated", TrustedPointCount: len(prepared.Points),
			SubmittedPointCount: len(prepared.SubmittedPoints), Warnings: prepared.ValidationWarnings, Errors: []ValidationError{}}}
	payloadJSON, _ := json.Marshal(payload)

	if existing == nil {
		_, err = conn.ExecContext(ctx, `INSERT INTO interactions
(interaction_id, glusr_id, role, channel, source, source_event_id, source_revision,
 occurred_at, received_at, updated_at, session_state, thread_id, summary,
 important_points_json, source_payload_json, processing_status, processed_revision,
 retry_count, next_retry_at, last_error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 0, 0, NULL, NULL)`,
			interactionIDValue, prepared.Request.GLUSRID, prepared.Request.Role, prepared.Request.Channel,
			prepared.Request.Source, prepared.Request.SourceEventID, revision, prepared.NormalizedTime, now, now,
			prepared.Request.SessionState, prepared.NormalizedThread, prepared.Enrichment.Summary,
			string(prepared.PointsJSON), string(payloadJSON))
	} else {
		_, err = conn.ExecContext(ctx, `UPDATE interactions SET source_revision = ?, received_at = ?, updated_at = ?,
session_state = ?, summary = ?, important_points_json = ?, source_payload_json = ?,
processing_status = 'pending', next_retry_at = NULL, last_error = NULL
WHERE interaction_id = ?`, revision, now, now, prepared.Request.SessionState, prepared.Enrichment.Summary,
			string(prepared.PointsJSON), string(payloadJSON), interactionIDValue)
	}
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Response{}, false, &ServiceError{Status: 409, Code: "source_identity_conflict", Message: "source identity is already bound to another interaction"}
		}
		return Response{}, false, unavailable(err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return Response{}, false, unavailable(err)
	}
	committed = true
	return Response{InteractionID: interactionIDValue, AcceptedRevision: revision, ProcessingStatus: "pending",
		Duplicate: prepared.DuplicateDelivery, AppliedChanges: applied, SkippedChanges: skipped, ConflictingChanges: conflicting,
		ValidationWarnings: prepared.ValidationWarnings, ExtractionStatus: "accepted", ValidationErrors: []ValidationError{}}, true, nil
}

func resolveThreads(ctx context.Context, conn *sql.Conn, prepared Prepared, interactionIDValue string) (Prepared, error) {
	owned := map[string]bool{}
	rows, err := conn.QueryContext(ctx, `SELECT thread_id,important_points_json FROM interactions WHERE glusr_id=? AND role=?`,
		prepared.Request.GLUSRID, prepared.Request.Role)
	if err != nil {
		return prepared, err
	}
	defer rows.Close()
	for rows.Next() {
		var rowThread sql.NullString
		var raw string
		if err := rows.Scan(&rowThread, &raw); err != nil {
			return prepared, err
		}
		if rowThread.Valid && rowThread.String != "" {
			owned[rowThread.String] = true
		}
		var points []Point
		if json.Unmarshal([]byte(raw), &points) == nil {
			for _, point := range points {
				if point.ThreadID != nil && *point.ThreadID != "" {
					owned[*point.ThreadID] = true
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return prepared, err
	}
	trusted := make([]Point, 0, len(prepared.Points))
	trustedIndexes := make([]int, 0, len(prepared.TrustedPointIndexes))
	for trustedIndex, originalIndex := range prepared.TrustedPointIndexes {
		point := prepared.Points[trustedIndex]
		if point.Scope == "thread" {
			switch {
			case point.ThreadID != nil && *point.ThreadID != "":
				if !owned[*point.ThreadID] && (prepared.NormalizedThread == nil || *prepared.NormalizedThread != *point.ThreadID) {
					prepared.PointEvidenceOK[originalIndex] = false
					prepared.ValidationWarnings = append(prepared.ValidationWarnings, ValidationWarning{Code: "thread_not_owned", PointIndex: originalIndex,
						Message: "thread_id is not owned by this GLID and role; point excluded from trusted memory"})
					continue
				}
			case point.Type == "issue":
				generated := fmt.Sprintf("thread:%s:%d", interactionIDValue, originalIndex)
				point.ThreadID = &generated
				prepared.SubmittedPoints[originalIndex].ThreadID = &generated
				owned[generated] = true
			case prepared.NormalizedThread != nil:
				value := *prepared.NormalizedThread
				point.ThreadID = &value
				prepared.SubmittedPoints[originalIndex].ThreadID = &value
			case point.Type == "resolution" || point.Type == "correction":
				prepared.PointEvidenceOK[originalIndex] = false
				prepared.ValidationWarnings = append(prepared.ValidationWarnings, ValidationWarning{Code: "missing_thread_id", PointIndex: originalIndex,
					Message: "thread resolution or correction requires an owned thread_id; point excluded from trusted memory"})
				continue
			}
		}
		trusted = append(trusted, point)
		trustedIndexes = append(trustedIndexes, originalIndex)
	}
	prepared.Points, prepared.TrustedPointIndexes = trusted, trustedIndexes
	prepared.PointsJSON, _ = json.Marshal(trusted)
	for index := range prepared.Updates {
		pointIndex := prepared.Updates[index].EvidencePointIndex
		if prepared.Updates[index].PrecheckReason == "" && (pointIndex < 0 || pointIndex >= len(prepared.PointEvidenceOK) || !prepared.PointEvidenceOK[pointIndex]) {
			prepared.Updates[index].PrecheckReason = "referenced_point_rejected"
		}
	}
	return prepared, nil
}

func loadExisting(ctx context.Context, conn *sql.Conn, prepared Prepared) (*existingInteraction, error) {
	var item existingInteraction
	var thread sql.NullString
	err := conn.QueryRowContext(ctx, `SELECT interaction_id, channel, occurred_at, session_state, thread_id,
source_revision, processed_revision, processing_status, source_payload_json
FROM interactions WHERE source = ? AND source_event_id = ? AND glusr_id = ? AND role = ?`,
		prepared.Request.Source, prepared.Request.SourceEventID, prepared.Request.GLUSRID, prepared.Request.Role).
		Scan(&item.InteractionID, &item.Channel, &item.OccurredAt, &item.SessionState, &thread,
			&item.SourceRevision, &item.ProcessedRevision, &item.ProcessingStatus, &item.SourcePayloadJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, unavailable(err)
	}
	if thread.Valid {
		item.ThreadID = &thread.String
	}
	return &item, nil
}

type latestChange struct {
	InteractionID string
	EffectiveAt   string
	Value         json.RawMessage
}

func loadLatestProfileChanges(ctx context.Context, conn *sql.Conn, glusrID, role, currentInteractionID string) (map[string]latestChange, error) {
	rows, err := conn.QueryContext(ctx, `SELECT interaction_id, occurred_at, source_payload_json
FROM interactions WHERE glusr_id = ? AND role = ? ORDER BY occurred_at DESC, interaction_id`, glusrID, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]latestChange{}
	for rows.Next() {
		var interactionIDValue, occurredAt, raw string
		if err := rows.Scan(&interactionIDValue, &occurredAt, &raw); err != nil {
			return nil, err
		}
		var payload sourcePayload
		if json.Unmarshal([]byte(raw), &payload) != nil {
			continue
		}
		changeSets := [][]StoredChange{payload.ProfileChanges}
		for _, history := range payload.RevisionHistory {
			changeSets = append(changeSets, history.ProfileChanges)
		}
		for _, changes := range changeSets {
			for _, change := range changes {
				if change.Status != "applied" {
					continue
				}
				current, exists := result[change.Path]
				if !exists || occurredAt > current.EffectiveAt || (occurredAt == current.EffectiveAt && interactionIDValue > current.InteractionID) {
					result[change.Path] = latestChange{InteractionID: interactionIDValue, EffectiveAt: occurredAt, Value: change.Value}
				}
			}
		}
	}
	return result, rows.Err()
}

func applyUpdates(profile map[string]any, prepared Prepared, revision int, latest map[string]latestChange) ([]StoredChange, []ChangeResult, []ChangeResult, []ChangeResult, bool) {
	stored := make([]StoredChange, 0, len(prepared.Updates))
	applied := []ChangeResult{}
	skipped := []ChangeResult{}
	conflicting := []ChangeResult{}
	changed := false
	interactionIDValue := interactionID(prepared)
	for _, update := range prepared.Updates {
		base := ChangeResult{Path: update.Path, Value: update.Value, EvidencePointIndex: update.EvidencePointIndex}
		entry := StoredChange{Path: update.Path, Value: update.Value, EvidencePointIndex: update.EvidencePointIndex,
			Evidence: update.Evidence, EffectiveAt: prepared.NormalizedTime, Revision: revision}
		if update.PrecheckReason != "" {
			base.Reason, entry.Reason, entry.Status = update.PrecheckReason, update.PrecheckReason, "skipped"
			skipped = append(skipped, base)
			stored = append(stored, entry)
			continue
		}
		if prior, exists := latest[update.Path]; exists && prior.InteractionID != interactionIDValue && prior.EffectiveAt > prepared.NormalizedTime && !rawEqual(prior.Value, update.Value) {
			base.Reason, entry.Reason, entry.Status = "newer_profile_change_exists", "newer_profile_change_exists", "conflicting"
			conflicting = append(conflicting, base)
			stored = append(stored, entry)
			continue
		}
		previous, existed := getPath(profile, update.Path)
		entry.PreviousExisted = existed
		if existed {
			entry.PreviousValue, _ = json.Marshal(previous)
		}
		var value any
		decoder := json.NewDecoder(bytes.NewReader(update.Value))
		decoder.UseNumber()
		_ = decoder.Decode(&value)
		if existed && valuesEqual(previous, value) {
			base.Reason, entry.Reason, entry.Status = "already_current", "already_current", "skipped"
			skipped = append(skipped, base)
			stored = append(stored, entry)
			continue
		}
		setPath(profile, update.Path, value)
		entry.Status = "applied"
		applied = append(applied, base)
		stored = append(stored, entry)
		changed = true
	}
	return stored, applied, skipped, conflicting, changed
}

func responseFromStored(existing *existingInteraction, changes []StoredChange, warnings []ValidationWarning, duplicate bool) Response {
	response := Response{InteractionID: existing.InteractionID, AcceptedRevision: existing.SourceRevision,
		ProcessingStatus: existing.ProcessingStatus, Duplicate: duplicate,
		AppliedChanges: []ChangeResult{}, SkippedChanges: []ChangeResult{}, ConflictingChanges: []ChangeResult{},
		ValidationWarnings: warnings, ExtractionStatus: "accepted", ValidationErrors: []ValidationError{}}
	for _, change := range changes {
		item := ChangeResult{Path: change.Path, Value: change.Value, EvidencePointIndex: change.EvidencePointIndex, Reason: change.Reason}
		switch change.Status {
		case "applied":
			response.AppliedChanges = append(response.AppliedChanges, item)
		case "conflicting":
			response.ConflictingChanges = append(response.ConflictingChanges, item)
		default:
			response.SkippedChanges = append(response.SkippedChanges, item)
		}
	}
	return response
}

func (s *Service) markProcessed(ctx context.Context, interactionIDValue string, revision int, glusrID, role string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE interactions SET processing_status = 'processed', processed_revision = ?,
retry_count = 0, next_retry_at = NULL, last_error = NULL WHERE interaction_id = ? AND source_revision = ?`,
		revision, interactionIDValue, revision)
	if err != nil {
		return 0, err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return 0, errors.New("interaction revision changed during rebuild")
	}
	var version int64
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM current_context WHERE glusr_id = ? AND role = ?`, glusrID, role).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

func (s *Service) markBuildFailed(ctx context.Context, interactionIDValue string, revision int) {
	next := shared.FormatUTC(s.now().UTC().Add(2 * time.Second))
	_, _ = s.db.ExecContext(ctx, `UPDATE interactions SET processing_status = 'failed', retry_count = retry_count + 1,
next_retry_at = ?, last_error = 'context_rebuild_failed' WHERE interaction_id = ? AND source_revision = ?`,
		next, interactionIDValue, revision)
}

func decodeObject(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, errors.New("JSON object required")
	}
	return value, nil
}

func getPath(root map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	current := root
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	value, ok := current[parts[len(parts)-1]]
	return value, ok
}

func setPath(root map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	current := root
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
}

func rawEqual(left, right json.RawMessage) bool {
	var a, b any
	leftDecoder, rightDecoder := json.NewDecoder(bytes.NewReader(left)), json.NewDecoder(bytes.NewReader(right))
	leftDecoder.UseNumber()
	rightDecoder.UseNumber()
	return leftDecoder.Decode(&a) == nil && rightDecoder.Decode(&b) == nil && valuesEqual(a, b)
}

func valuesEqual(left, right any) bool {
	a, _ := json.Marshal(left)
	b, _ := json.Marshal(right)
	return bytes.Equal(a, b)
}

func sameStringPointer(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func unavailable(_ error) error {
	return &ServiceError{Status: 503, Code: "storage_unavailable", Message: "enrichment storage is temporarily unavailable"}
}
