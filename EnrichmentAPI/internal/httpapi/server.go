package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"vani-context/enrichmentapi/internal/enrich"
	"vani-context/enrichmentapi/shared"
)

const maxBodyBytes = 1 << 20

type Server struct {
	service        *enrich.Service
	dbReady        func(context.Context) error
	logger         *slog.Logger
	mux            *http.ServeMux
	sourceTimezone *time.Location
}

func New(service *enrich.Service, token string, logger *slog.Logger, ready func(context.Context) error, sourceTimezone *time.Location) *Server {
	server := &Server{service: service, dbReady: ready, logger: logger, mux: http.NewServeMux(), sourceTimezone: sourceTimezone}
	server.mux.HandleFunc("GET /health", server.health)
	server.mux.Handle("POST /interactions/enrich", shared.Authenticate(token, http.HandlerFunc(server.enrich)))
	return server
}

func (s *Server) Handler() http.Handler { return shared.WithRequestLogging(s.logger, s.mux) }

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.dbReady(ctx); err != nil {
		shared.WriteError(w, 503, "storage_unavailable", "database is not ready")
		return
	}
	shared.WriteJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) enrich(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		s.logger.Warn("enrichment request rejected before decode", "code", "invalid_content_type",
			"content_type", r.Header.Get("Content-Type"), "content_length", r.ContentLength)
		shared.WriteError(w, 400, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input enrich.Request
	if err := decoder.Decode(&input); err != nil {
		s.logger.Warn("enrichment request rejected before decode", "code", "invalid_json",
			"content_length", r.ContentLength, "decode_error", err.Error())
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			shared.WriteError(w, 413, "request_too_large", "request body exceeds 1 MiB")
		} else {
			shared.WriteError(w, 400, "invalid_json", "request must contain one valid JSON object with documented fields")
		}
		return
	}
	if err := requireEOF(decoder); err != nil {
		s.logger.Warn("enrichment request rejected before decode", "code", "invalid_json",
			"content_length", r.ContentLength, "decode_error", "request contains data after the first JSON object")
		shared.WriteError(w, 400, "invalid_json", "request must contain exactly one JSON object")
		return
	}
	s.logger.Info("enrichment request received",
		"glusr_id", input.GLUSRID,
		"role", input.Role,
		"source", input.Source,
		"source_event_id", input.SourceEventID,
		"channel", input.Channel,
		"occurred_at", input.OccurredAt,
		"start_datetime", input.StartDatetime,
		"session_state", input.SessionState,
		"thread_id", input.ThreadID,
		"transcript_bytes", len(input.Transcript),
		"enrichment_json_bytes", len(input.EnrichmentJSON),
	)
	prepared, code, err := enrich.ValidateEnvelope(input, s.sourceTimezone)
	if err != nil {
		s.logger.Warn("enrichment request rejected", "code", code, "reason", err.Error())
		shared.WriteError(w, 400, code, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	prepared, completed, completedStatus, err := s.service.AcceptRaw(ctx, prepared)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if completed != nil {
		shared.WriteJSON(w, completedStatus, completed)
		return
	}
	prepared, code, err = enrich.ValidateExtraction(prepared)
	if err != nil {
		s.logger.Warn("enrichment extraction rejected after durable raw acceptance", "code", code,
			"reason", err.Error(), "interaction_id", prepared.InteractionID, "accepted_revision", prepared.AcceptedRevision)
		response, status, persistErr := s.service.RejectExtraction(ctx, prepared, code, err.Error())
		if persistErr != nil {
			s.writeServiceError(w, persistErr)
			return
		}
		shared.WriteJSON(w, status, response)
		return
	}
	response, status, err := s.service.Process(ctx, prepared)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	shared.WriteJSON(w, status, response)
}

func (s *Server) writeServiceError(w http.ResponseWriter, err error) {
	var serviceError *enrich.ServiceError
	if errors.As(err, &serviceError) {
		if serviceError.Status >= 500 {
			s.logger.Error("enrichment failed", "error", err)
		}
		shared.WriteError(w, serviceError.Status, serviceError.Code, serviceError.Message)
		return
	}
	s.logger.Error("enrichment failed", "error", err)
	shared.WriteError(w, 503, "storage_unavailable", "enrichment storage is temporarily unavailable")
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return errors.New("extra JSON")
}
