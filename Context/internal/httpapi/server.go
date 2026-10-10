package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"vani-context/internal/storage"
)

var glusrIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type Server struct {
	repository storage.ContextRepository
	token      string
	logger     *slog.Logger
	mux        *http.ServeMux
}

type errorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func New(repository storage.ContextRepository, token string, logger *slog.Logger) *Server {
	server := &Server{repository: repository, token: token, logger: logger, mux: http.NewServeMux()}
	server.mux.HandleFunc("GET /health", server.health)
	server.mux.Handle("GET /v1/context", server.authenticate(http.HandlerFunc(server.getContext)))
	return server
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		loggedWriter := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			s.logger.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", loggedWriter.status,
				"duration_ms", time.Since(started).Milliseconds(),
			)
		}()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		s.mux.ServeHTTP(loggedWriter, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.repository.Ready(ctx); err != nil {
		s.logger.Error("health check failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "database is not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) getContext(w http.ResponseWriter, r *http.Request) {
	if err := validateQueryKeys(r, "glusr_id", "role"); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	glusrID, err := oneQueryValue(r, "glusr_id")
	if err != nil || !glusrIDPattern.MatchString(glusrID) {
		writeError(w, http.StatusBadRequest, "invalid_identity", "glusr_id must be 1-128 characters using letters, numbers, dot, underscore, colon, or hyphen")
		return
	}
	role, err := oneQueryValue(r, "role")
	if err != nil || (role != "buyer" && role != "seller") {
		writeError(w, http.StatusBadRequest, "invalid_role", "role must be buyer or seller")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	snapshot, err := s.repository.GetPreparedContext(ctx, glusrID, role)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, http.StatusNotFound, "context_not_found", "no published context exists for this GLID and role")
		return
	case errors.Is(err, storage.ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "context storage is temporarily unavailable")
		return
	case err != nil:
		s.logger.Error("context retrieval failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "context storage is temporarily unavailable")
		return
	default:
		writeJSON(w, http.StatusOK, snapshot)
	}
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) || len(header) == len(prefix) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
			return
		}
		provided := header[len(prefix):]
		if len(provided) != len(s.token) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validateQueryKeys(r *http.Request, allowed ...string) error {
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key := range r.URL.Query() {
		if _, ok := set[key]; !ok {
			return errors.New("unsupported query parameter: " + key)
		}
	}
	return nil
}

func oneQueryValue(r *http.Request, key string) (string, error) {
	values, ok := r.URL.Query()[key]
	if !ok || len(values) != 1 || values[0] == "" || strings.TrimSpace(values[0]) != values[0] {
		return "", errors.New("missing or duplicate query parameter: " + key)
	}
	return values[0], nil
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Error: apiError{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
