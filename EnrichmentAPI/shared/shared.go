package shared

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var IdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var SourceEventIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)

type APIError struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func OpenSQLite(ctx context.Context, path string, readOnly bool) (*sql.DB, error) {
	dsn := path
	if readOnly {
		dsn = (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	statements := []string{"PRAGMA foreign_keys = ON", "PRAGMA busy_timeout = 5000"}
	if readOnly {
		statements = append(statements, "PRAGMA query_only = ON")
	} else {
		statements = append(statements, "PRAGMA journal_mode = WAL")
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure sqlite: %w", err)
		}
	}
	return db, nil
}

func Authenticate(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) || len(header) == len(prefix) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			WriteError(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
			return
		}
		provided := header[len(prefix):]
		if len(provided) != len(token) || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			WriteError(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func WithRequestLogging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		wrapped := &StatusWriter{ResponseWriter: w, Status: http.StatusOK}
		defer func() {
			logger.Info("http request", "method", r.Method, "path", r.URL.Path,
				"status", wrapped.Status, "duration_ms", time.Since(started).Milliseconds())
		}()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(wrapped, r)
	})
}

type StatusWriter struct {
	http.ResponseWriter
	Status      int
	wroteHeader bool
}

func (w *StatusWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.Status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *StatusWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, APIError{Error: ErrorBody{Code: code, Message: message}})
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func ParseRole(role string) error {
	if role != "buyer" && role != "seller" {
		return errors.New("role must be buyer or seller")
	}
	return nil
}

func FormatUTC(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}
