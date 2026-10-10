package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"vani-lookup/internal/search"
	"vani-lookup/internal/store"
)

const maxBodyBytes = 16 << 10

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type request struct {
	GLUSRID      string        `json:"glusr_id"`
	Role         string        `json:"role"`
	Topic        *string       `json:"topic,omitempty"`
	TopicKeyword *string       `json:"topic_keyword,omitempty"`
	LookbackDays *textOrNumber `json:"lookback_days,omitempty"`
	ThreadID     *string       `json:"thread_id,omitempty"`
	From         *string       `json:"from,omitempty"`
	To           *string       `json:"to,omitempty"`
	Limit        *int          `json:"limit,omitempty"`
}

type textOrNumber int

func (value *textOrNumber) UnmarshalJSON(data []byte) error {
	var number int
	if err := json.Unmarshal(data, &number); err == nil {
		*value = textOrNumber(number)
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return errors.New("must be an integer or integer text")
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return errors.New("must be integer text")
	}
	*value = textOrNumber(parsed)
	return nil
}

type toolResponse struct {
	Found   bool   `json:"found"`
	Context string `json:"context"`
}

type Server struct {
	store         *store.Store
	engine        *search.Engine
	token         string
	logger        *slog.Logger
	referenceTime func() time.Time
	mux           *http.ServeMux
}

func New(repository *store.Store, engine *search.Engine, token string, logger *slog.Logger, referenceTime func() time.Time) *Server {
	if referenceTime == nil {
		referenceTime = time.Now
	}
	server := &Server{store: repository, engine: engine, token: token, logger: logger, referenceTime: referenceTime, mux: http.NewServeMux()}
	server.mux.HandleFunc("GET /health", server.health)
	server.mux.Handle("POST /v1/history/lookup", server.authenticate(http.HandlerFunc(server.lookup)))
	return server
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		logged := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			s.logger.Info("http request", "method", r.Method, "path", r.URL.Path,
				"status", logged.status, "duration_ms", time.Since(started).Milliseconds())
		}()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		s.mux.ServeHTTP(logged, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.store.Ready(ctx); err != nil {
		s.logger.Error("health check failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "shared memory database is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) lookup(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusBadRequest, "invalid_content_type", "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 16 KiB")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_json", "request body could not be read")
		}
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var input request
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request must contain one valid JSON object with documented fields")
		return
	}
	if err := ensureEOF(decoder); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request must contain exactly one JSON object")
		return
	}
	query, validationCode, validationMessage := s.validate(input)
	if validationCode != "" {
		writeError(w, http.StatusBadRequest, validationCode, validationMessage)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	response, err := s.engine.Search(ctx, query)
	switch {
	case errors.Is(err, search.ErrThreadNotOwned):
		writeError(w, http.StatusForbidden, "thread_not_owned", "thread_id does not belong to the requested GLID and role")
	case errors.Is(err, store.ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "history storage is temporarily unavailable")
	case err != nil:
		s.logger.Error("history lookup failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "lookup_failed", "history lookup could not be completed")
	default:
		writeJSON(w, http.StatusOK, toolResponse{Found: response.Status == "found", Context: renderToolContext(response)})
	}
}

func renderToolContext(response search.Response) string {
	if response.Status != "found" || len(response.Matches) == 0 {
		return "No matching historical evidence was found for this GLID and role in the requested period."
	}
	var result strings.Builder
	result.WriteString("Historical evidence:\n")
	for index, match := range response.Matches {
		result.WriteString("\nMatch ")
		result.WriteString(strconv.Itoa(index + 1))
		result.WriteString(":\n- Date: ")
		result.WriteString(match.OccurredAt)
		result.WriteString("\n- Channel: ")
		result.WriteString(match.Channel)
		if match.ThreadID != nil {
			result.WriteString("\n- Thread: ")
			result.WriteString(*match.ThreadID)
		}
		result.WriteString("\n- Source: ")
		result.WriteString(match.Source)
		result.WriteString(" / ")
		result.WriteString(match.SourceEventID)
		if match.ExtractionPending {
			result.WriteString("\n- Extraction: rejected or pending; raw transcript evidence below is not trusted structured memory")
		}
		if match.Summary != "" {
			result.WriteString("\n- Summary: ")
			result.WriteString(match.Summary)
		}
		for _, point := range match.ImportantPoints {
			result.WriteString("\n- Point: type=")
			result.WriteString(point.Type)
			result.WriteString(", field=")
			result.WriteString(point.Field)
			result.WriteString(", status=")
			result.WriteString(point.Status)
			if encoded, err := json.Marshal(point.Value); err == nil {
				result.WriteString(", value=")
				result.Write(encoded)
			}
			if point.Unit != nil {
				result.WriteString(", unit=")
				result.WriteString(*point.Unit)
			}
			if point.Evidence != "" {
				result.WriteString("\n- Evidence: ")
				result.WriteString(point.Evidence)
			}
		}
		if len(match.ImportantPoints) == 0 {
			for _, evidence := range match.SupportingEvidence {
				result.WriteString("\n- Supporting transcript evidence: ")
				result.WriteString(evidence)
			}
		}
	}
	if len(response.RelatedUpdates) > 0 {
		result.WriteString("\n\nLinked later updates:")
		for _, update := range response.RelatedUpdates {
			result.WriteString("\n- ")
			result.WriteString(update.OccurredAt)
			result.WriteString(" [")
			result.WriteString(update.Channel)
			result.WriteString("] ")
			result.WriteString(update.Point.Type)
			result.WriteString(" for ")
			result.WriteString(update.Point.Field)
			if update.ThreadID != nil {
				result.WriteString(" in thread ")
				result.WriteString(*update.ThreadID)
			}
			if encoded, err := json.Marshal(update.Point.Value); err == nil {
				result.WriteString(": ")
				result.Write(encoded)
			}
			if update.Point.Evidence != "" {
				result.WriteString(". Evidence: ")
				result.WriteString(update.Point.Evidence)
			}
			result.WriteString(". Source: ")
			result.WriteString(update.SourceEventID)
			result.WriteString(".")
		}
	}
	if response.SearchScopeLimited || response.RelatedUpdatesLimited || response.ResponseTruncated {
		result.WriteString("\n\nNotice: Results were bounded; additional retained history may exist.")
	}
	return result.String()
}

func (s *Server) validate(input request) (search.Query, string, string) {
	if !identityPattern.MatchString(input.GLUSRID) {
		return search.Query{}, "invalid_identity", "glusr_id must be 1-128 characters using letters, numbers, dot, underscore, colon, or hyphen"
	}
	if input.Role != "buyer" && input.Role != "seller" {
		return search.Query{}, "invalid_role", "role must be buyer or seller"
	}
	if input.Topic != nil && input.TopicKeyword != nil {
		return search.Query{}, "conflicting_topic_fields", "supply topic or topic_keyword, not both"
	}
	topic := ""
	if input.Topic != nil {
		topic = strings.TrimSpace(*input.Topic)
	} else if input.TopicKeyword != nil {
		topic = strings.TrimSpace(*input.TopicKeyword)
	}
	if len([]rune(topic)) > 512 {
		return search.Query{}, "invalid_topic", "topic must not exceed 512 characters"
	}
	var threadID *string
	if input.ThreadID != nil {
		value := strings.TrimSpace(*input.ThreadID)
		if !identityPattern.MatchString(value) {
			return search.Query{}, "invalid_thread", "thread_id has an invalid format"
		}
		threadID = &value
	}
	if len(search.Terms(topic)) == 0 && threadID == nil {
		return search.Query{}, "invalid_topic", "a meaningful topic or owned thread_id is required"
	}
	if input.LookbackDays != nil && (input.From != nil || input.To != nil) {
		return search.Query{}, "conflicting_date_fields", "lookback_days cannot be combined with from or to"
	}
	from, to := "", ""
	if input.LookbackDays != nil {
		lookbackDays := int(*input.LookbackDays)
		if lookbackDays < 1 || lookbackDays > 3650 {
			return search.Query{}, "invalid_lookback", "lookback_days must be from 1 to 3650"
		}
		reference := s.referenceTime().UTC()
		from = store.FormatUTC(reference.AddDate(0, 0, -lookbackDays))
		to = store.FormatUTC(reference.Add(time.Millisecond))
	} else {
		var fromTime, toTime *time.Time
		if input.From != nil {
			parsed, err := time.Parse(time.RFC3339Nano, *input.From)
			if err != nil {
				return search.Query{}, "invalid_date_range", "from must be a timezone-aware RFC3339 timestamp"
			}
			value := parsed.UTC()
			fromTime = &value
			from = store.FormatUTC(value)
		}
		if input.To != nil {
			parsed, err := time.Parse(time.RFC3339Nano, *input.To)
			if err != nil {
				return search.Query{}, "invalid_date_range", "to must be a timezone-aware RFC3339 timestamp"
			}
			value := parsed.UTC()
			toTime = &value
			to = store.FormatUTC(value)
		}
		if fromTime != nil && toTime != nil && !fromTime.Before(*toTime) {
			return search.Query{}, "invalid_date_range", "from must be earlier than to"
		}
	}
	limit := 3
	if input.Limit != nil {
		limit = *input.Limit
	}
	if limit < 1 || limit > 5 {
		return search.Query{}, "invalid_limit", "limit must be from 1 to 5"
	}
	return search.Query{GLUSRID: input.GLUSRID, Role: input.Role, Topic: topic,
		ThreadID: threadID, From: from, To: to, Limit: limit}, "", ""
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

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status, w.wroteHeader = status, true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return errors.New("extra JSON value")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
