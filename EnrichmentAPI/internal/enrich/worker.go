package enrich

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"vani-context/enrichmentapi/shared"
	contextstorage "vani-context/internal/storage"
)

func (s *Service) RunWorker(ctx context.Context, interval time.Duration, logger *slog.Logger) {
	s.retryOnce(ctx, logger)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.retryOnce(ctx, logger)
		}
	}
}

func (s *Service) retryOnce(ctx context.Context, logger *slog.Logger) {
	s.recoverPendingValidation(ctx, logger)
	now := shared.FormatUTC(s.now().UTC())
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT glusr_id, role FROM interactions
WHERE processing_status = 'failed' AND last_error = 'context_rebuild_failed' AND next_retry_at <= ?
ORDER BY glusr_id, role LIMIT 20`, now)
	if err != nil {
		logger.Error("enrichment retry scan failed", "error", err)
		return
	}
	type key struct{ glusrID, role string }
	var keys []key
	for rows.Next() {
		var item key
		if err := rows.Scan(&item.glusrID, &item.role); err != nil {
			rows.Close()
			logger.Error("enrichment retry scan failed", "error", err)
			return
		}
		keys = append(keys, item)
	}
	rows.Close()
	for _, item := range keys {
		if err := s.builder.Rebuild(ctx, contextstorage.UserKey{GLUSRID: item.glusrID, Role: item.role}); err != nil {
			next := shared.FormatUTC(s.now().UTC().Add(5 * time.Second))
			_, _ = s.db.ExecContext(ctx, `UPDATE interactions SET processing_status='failed',
retry_count=retry_count+1, next_retry_at=?, last_error='context_rebuild_failed'
WHERE glusr_id=? AND role=? AND processing_status IN ('pending','failed')`, next, item.glusrID, item.role)
			logger.Error("enrichment context retry failed", "glusr_id", item.glusrID, "role", item.role, "error", err)
			continue
		}
		_, err := s.db.ExecContext(ctx, `UPDATE interactions SET processing_status='processed',
processed_revision=source_revision, retry_count=0, next_retry_at=NULL, last_error=NULL
WHERE glusr_id=? AND role=? AND processing_status IN ('pending','failed')`, item.glusrID, item.role)
		if err != nil {
			logger.Error("enrichment retry finalization failed", "glusr_id", item.glusrID, "role", item.role, "error", err)
		}
	}
}

func (s *Service) recoverPendingValidation(ctx context.Context, logger *slog.Logger) {
	rows, err := s.db.QueryContext(ctx, `SELECT interaction_id,glusr_id,role,channel,source,source_event_id,
source_revision,occurred_at,session_state,thread_id,source_payload_json
FROM interactions WHERE processing_status='pending' ORDER BY received_at LIMIT 20`)
	if err != nil {
		logger.Error("enrichment pending scan failed", "error", err)
		return
	}
	type pending struct {
		interactionID, glusrID, role, channel, source, sourceEventID, occurredAt, sessionState, payload string
		revision                                                                                        int
		threadID                                                                                        *string
	}
	items := []pending{}
	for rows.Next() {
		var item pending
		var thread sql.NullString
		if err := rows.Scan(&item.interactionID, &item.glusrID, &item.role, &item.channel, &item.source, &item.sourceEventID,
			&item.revision, &item.occurredAt, &item.sessionState, &thread, &item.payload); err != nil {
			rows.Close()
			logger.Error("enrichment pending scan failed", "error", err)
			return
		}
		if thread.Valid {
			item.threadID = &thread.String
		}
		items = append(items, item)
	}
	rows.Close()
	for _, item := range items {
		var payload sourcePayload
		if json.Unmarshal([]byte(item.payload), &payload) != nil {
			logger.Error("enrichment pending provenance invalid", "interaction_id", item.interactionID)
			continue
		}
		prepared := Prepared{Request: Request{GLUSRID: item.glusrID, Role: item.role, Channel: item.channel, Source: item.source,
			SourceEventID: item.sourceEventID, OccurredAt: item.occurredAt, SessionState: item.sessionState, ThreadID: item.threadID,
			Transcript: payload.Transcript, TranscriptRaw: payload.TranscriptRaw, TranscriptTurns: payload.TranscriptTurns,
			EnrichmentJSON: payload.EnrichmentJSON, EnrichmentRaw: payload.EnrichmentRaw}, OriginalSource: payload.InputAdapter.OriginalSource,
			OriginalChannel: payload.InputAdapter.OriginalChannel, CanonicalHash: payload.CanonicalHash, NormalizedTime: item.occurredAt,
			NormalizedThread: item.threadID, EventTime: payload.EventTime, InteractionID: item.interactionID, AcceptedRevision: item.revision,
			DuplicateDelivery: true}
		validated, code, validationErr := ValidateExtraction(prepared)
		if validationErr != nil {
			if _, _, err := s.RejectExtraction(ctx, prepared, code, validationErr.Error()); err != nil {
				logger.Error("enrichment pending rejection failed", "interaction_id", item.interactionID, "error", err)
			}
			continue
		}
		if _, _, err := s.Process(ctx, validated); err != nil {
			logger.Error("enrichment pending recovery failed", "interaction_id", item.interactionID, "error", err)
		}
	}
}
