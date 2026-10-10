package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"vani-context/enrichmentapi/shared"
)

type Server struct {
	db     *sql.DB
	logger *slog.Logger
	mux    *http.ServeMux
}

type profileResponse struct {
	GLUSRID          string          `json:"glusr_id"`
	Role             string          `json:"role"`
	BusinessName     *string         `json:"business_name"`
	City             *string         `json:"city"`
	ProfileJSON      json.RawMessage `json:"profile_json"`
	ProfileUpdatedAt string          `json:"profile_updated_at"`
}

func New(db *sql.DB, token string, logger *slog.Logger) *Server {
	server := &Server{db: db, logger: logger, mux: http.NewServeMux()}
	server.mux.HandleFunc("GET /health", server.health)
	server.mux.Handle("GET /users/{glusr_id}/profile", shared.Authenticate(token, http.HandlerFunc(server.profile)))
	return server
}

func (s *Server) Handler() http.Handler { return shared.WithRequestLogging(s.logger, s.mux) }

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	var one int
	if err := s.db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		shared.WriteError(w, 503, "storage_unavailable", "database is not ready")
		return
	}
	shared.WriteJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	glusrID := r.PathValue("glusr_id")
	if !shared.IdentifierPattern.MatchString(glusrID) {
		shared.WriteError(w, 400, "invalid_identity", "glusr_id has an invalid format")
		return
	}
	if len(r.URL.Query()) != 1 || len(r.URL.Query()["role"]) != 1 {
		shared.WriteError(w, 400, "invalid_request", "exactly one role query parameter is required")
		return
	}
	role := r.URL.Query().Get("role")
	if err := shared.ParseRole(role); err != nil {
		shared.WriteError(w, 400, "invalid_role", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var response profileResponse
	var businessName, city sql.NullString
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT glusr_id, role, business_name, city, profile_json, profile_updated_at
FROM users WHERE glusr_id = ? AND role = ?`, glusrID, role).Scan(&response.GLUSRID, &response.Role,
		&businessName, &city, &raw, &response.ProfileUpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		shared.WriteError(w, 404, "user_not_found", "no user exists for this GLID and role")
		return
	}
	if err != nil {
		s.logger.Error("profile retrieval failed", "error", err)
		shared.WriteError(w, 503, "storage_unavailable", "profile storage is temporarily unavailable")
		return
	}
	if !json.Valid([]byte(raw)) {
		s.logger.Error("stored profile JSON is invalid", "glusr_id", glusrID, "role", role)
		shared.WriteError(w, 503, "invalid_stored_profile", "stored profile is unavailable")
		return
	}
	if businessName.Valid {
		response.BusinessName = &businessName.String
	}
	if city.Valid {
		response.City = &city.String
	}
	response.ProfileJSON = json.RawMessage(raw)
	shared.WriteJSON(w, 200, response)
}
