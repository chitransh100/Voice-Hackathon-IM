package httpapi_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vani-lookup/internal/httpapi"
	"vani-lookup/internal/search"
	"vani-lookup/internal/store"

	_ "modernc.org/sqlite"
)

const testToken = "lookup-test-token-123456789"

type fixture struct {
	handler http.Handler
	store   *store.Store
	dbPath  string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "vani.sqlite")
	seedActualSchema(t, dbPath)
	repository, err := store.OpenReadOnly(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repository.Close() })
	reference := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := search.New(repository, 500)
	return fixture{handler: httpapi.New(repository, engine, testToken, logger, func() time.Time { return reference }).Handler(), store: repository, dbPath: dbPath}
}

func seedActualSchema(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema, err := os.ReadFile("../../../Context/migrations/001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	users := []struct{ id, role, name string }{
		{"SYNTH-LOOKUP-001", "seller", "Synthetic Lookup Seller One"},
		{"SYNTH-LOOKUP-001", "buyer", "Synthetic Lookup Buyer One"},
		{"SYNTH-LOOKUP-002", "seller", "Synthetic Lookup Seller Two"},
	}
	for _, user := range users {
		if _, err := db.Exec(`INSERT INTO users
(glusr_id, role, business_name, city, profile_json, profile_updated_at)
VALUES (?, ?, ?, 'Synthetic City', '{"data_classification":"synthetic_sample"}', '2026-10-09T00:00:00.000Z')`, user.id, user.role, user.name); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO current_context
(glusr_id, role, memory_json, context_md, version, updated_at, token_count, token_count_method, source_coverage_json)
VALUES (?, ?, '{}', ?, 1, '2026-10-09T00:00:00.000Z', 20, 'estimated', '{"synthetic":true}')`,
			user.id, user.role, "Current context intentionally excludes the older duplicate-product incident."); err != nil {
			t.Fatal(err)
		}
	}
	fixtures := []interactionFixture{
		{
			id: "lookup-old-duplicate", user: "SYNTH-LOOKUP-001", role: "seller", channel: "web_chat",
			sourceID: "synthetic-chat-old-duplicate", occurred: "2026-08-10T10:00:00.000Z", thread: stringPointer("catalogue-a"),
			summary: "Seller reported duplicate products after catalogue upload.",
			points:  `[{"type":"issue","scope":"thread","thread_id":"catalogue-a","field":"catalogue_upload","value":"Duplicate products appeared","unit":null,"status":"reported","keywords":["catalogue","upload","duplicate"],"evidence":"My catalogue upload created duplicate products."}]`,
		},
		{
			id: "lookup-later-resolution", user: "SYNTH-LOOKUP-001", role: "seller", channel: "voice",
			sourceID: "synthetic-call-resolution", occurred: "2026-09-01T09:00:00.000Z", thread: stringPointer("catalogue-a"),
			summary: "Seller confirmed the duplicate catalogue issue was resolved.",
			points:  `[{"type":"resolution","scope":"thread","thread_id":"catalogue-a","field":"catalogue_upload","value":"Duplicate products removed","unit":null,"status":"confirmed","keywords":["catalogue","duplicate","resolved"],"evidence":"The duplicate catalogue products are removed now."}]`,
		},
		{
			id: "lookup-thread-a-quantity", user: "SYNTH-LOOKUP-001", role: "seller", channel: "enquiry",
			sourceID: "synthetic-enquiry-a", occurred: "2026-09-15T09:00:00.000Z", thread: stringPointer("pump-enquiry-a"),
			summary: "First pump enquiry requested 100 pieces.",
			points:  `[{"type":"fact","scope":"thread","thread_id":"pump-enquiry-a","field":"quantity","value":100,"unit":"pieces","status":"confirmed","keywords":["pump","quantity"],"evidence":"For enquiry A, I need 100 pump pieces."}]`,
		},
		{
			id: "lookup-thread-a-correction", user: "SYNTH-LOOKUP-001", role: "seller", channel: "voice",
			sourceID: "synthetic-enquiry-a-correction", occurred: "2026-09-20T09:00:00.000Z", thread: stringPointer("pump-enquiry-a"),
			summary: "Seller corrected enquiry A quantity to 200 pieces.",
			points:  `[{"type":"correction","scope":"thread","thread_id":"pump-enquiry-a","field":"quantity","value":200,"unit":"pieces","status":"confirmed","keywords":["pump","quantity"],"evidence":"Actually, make enquiry A 200 pump pieces."}]`,
		},
		{
			id: "lookup-thread-b-quantity", user: "SYNTH-LOOKUP-001", role: "seller", channel: "enquiry",
			sourceID: "synthetic-enquiry-b", occurred: "2026-09-16T09:00:00.000Z", thread: stringPointer("pump-enquiry-b"),
			summary: "Second pump enquiry requested 500 pieces.",
			points:  `[{"type":"fact","scope":"thread","thread_id":"pump-enquiry-b","field":"quantity","value":500,"unit":"pieces","status":"confirmed","keywords":["pump","quantity"],"evidence":"For enquiry B, I need 500 pump pieces."}]`,
		},
		{
			id: "lookup-recent-omitted", user: "SYNTH-LOOKUP-001", role: "seller", channel: "whatsapp",
			sourceID: "synthetic-recent-blue", occurred: "2026-10-08T09:00:00.000Z", thread: stringPointer("pump-enquiry-a"),
			summary: "Seller asked for the blue pump variant; this detail is omitted from prepared context.",
			points:  `[{"type":"preference","scope":"thread","thread_id":"pump-enquiry-a","field":"other","value":"blue pump variant","unit":null,"status":"reported","keywords":["blue","pump","variant"],"evidence":"Please use the blue pump variant."}]`,
		},
		{
			id: "lookup-other-seller", user: "SYNTH-LOOKUP-002", role: "seller", channel: "voice",
			sourceID: "synthetic-other-seller", occurred: "2026-08-10T10:00:00.000Z", thread: stringPointer("catalogue-other"),
			summary: "Other seller mentioned a confidential orange catalogue marker.",
			points:  `[{"type":"fact","scope":"thread","thread_id":"catalogue-other","field":"other","value":"orange marker","unit":null,"status":"confirmed","keywords":["orange","marker"],"evidence":"My marker is orange."}]`,
		},
		{
			id: "lookup-buyer-only", user: "SYNTH-LOOKUP-001", role: "buyer", channel: "enquiry",
			sourceID: "synthetic-buyer-only", occurred: "2026-08-11T10:00:00.000Z", thread: stringPointer("buyer-thread"),
			summary: "Buyer used the private violet buyer marker.",
			points:  `[{"type":"fact","scope":"thread","thread_id":"buyer-thread","field":"other","value":"violet buyer marker","unit":null,"status":"confirmed","keywords":["violet","buyer","marker"],"evidence":"My buyer marker is violet."}]`,
		},
	}
	for _, item := range fixtures {
		item.insert(t, db)
	}
}

type interactionFixture struct {
	id, user, role, channel, sourceID, occurred, summary, points string
	thread                                                       *string
}

func (item interactionFixture) insert(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO interactions
(interaction_id, glusr_id, role, channel, source, source_event_id, source_revision,
 occurred_at, received_at, updated_at, session_state, thread_id, summary,
 important_points_json, source_payload_json, processing_status, processed_revision)
VALUES (?, ?, ?, ?, 'synthetic_lookup_fixture', ?, 1, ?, ?, ?, 'completed', ?, ?, ?, '{}', 'processed', 1)`,
		item.id, item.user, item.role, item.channel, item.sourceID, item.occurred, item.occurred,
		item.occurred, item.thread, item.summary, item.points)
	if err != nil {
		t.Fatal(err)
	}
}

func post(t *testing.T, handler http.Handler, body string, token string) (*http.Response, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/history/lookup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	response := recorder.Result()
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, result
}

func TestOlderHistoryAbsentFromContextAndLaterResolution(t *testing.T) {
	fx := newFixture(t)
	response, body := post(t, fx.handler, `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic_keyword":"catalog duplicate","lookback_days":90}`, testToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	text := string(body)
	if !strings.Contains(text, `"found":true`) || !strings.Contains(text, "synthetic-chat-old-duplicate") ||
		!strings.Contains(text, "synthetic-call-resolution") || !strings.Contains(text, "Linked later updates") ||
		!strings.Contains(text, "resolution for catalogue_upload") {
		t.Fatalf("old evidence or linked resolution missing: %s", body)
	}
	db, err := sql.Open("sqlite", fx.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var contextMD string
	if err := db.QueryRow(`SELECT context_md FROM current_context WHERE glusr_id = ? AND role = ?`, "SYNTH-LOOKUP-001", "seller").Scan(&contextMD); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(contextMD, "synthetic-chat-old-duplicate") || strings.Contains(contextMD, "My catalogue upload created duplicate products") {
		t.Fatalf("older evidence unexpectedly present in current context: %s", contextMD)
	}
}

func TestLookbackExcludesOlderHistory(t *testing.T) {
	fx := newFixture(t)
	_, body := post(t, fx.handler, `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic_keyword":"duplicate","lookback_days":30}`, testToken)
	if !strings.Contains(string(body), `"found":false`) || !strings.Contains(string(body), "No matching historical evidence") {
		t.Fatalf("older evidence escaped lookback: %s", body)
	}
}

func TestNoMatch(t *testing.T) {
	fx := newFixture(t)
	_, body := post(t, fx.handler, `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic":"zebra unicorn","limit":3}`, testToken)
	if !strings.Contains(string(body), `"found":false`) || !strings.Contains(string(body), "No matching historical evidence") {
		t.Fatalf("unexpected no-match response: %s", body)
	}
}

func TestInputValidationAndAuthentication(t *testing.T) {
	fx := newFixture(t)
	tests := []struct {
		name, body string
		status     int
	}{
		{"role", `{"glusr_id":"SYNTH-LOOKUP-001","role":"admin","topic":"pump"}`, 400},
		{"lookback", `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic":"pump","lookback_days":0}`, 400},
		{"text lookback accepted", `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic":"pump","lookback_days":"90"}`, 200},
		{"invalid text lookback", `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic":"pump","lookback_days":"ninety"}`, 400},
		{"limit", `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic":"pump","limit":6}`, 400},
		{"conflict", `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic":"pump","topic_keyword":"pump"}`, 400},
		{"sql text is ordinary input", `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic":"' OR 1=1 --"}`, 200},
		{"unknown field", `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic":"pump","evil":true}`, 400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, _ := post(t, fx.handler, test.body, testToken)
			if response.StatusCode != test.status {
				t.Fatalf("status=%d", response.StatusCode)
			}
		})
	}
	response, _ := post(t, fx.handler, `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic":"pump"}`, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", response.StatusCode)
	}
}

func TestBuyerSellerAndDifferentUserIsolation(t *testing.T) {
	fx := newFixture(t)
	_, seller := post(t, fx.handler, `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic":"violet buyer marker"}`, testToken)
	if !strings.Contains(string(seller), `"found":false`) {
		t.Fatalf("buyer data leaked to seller: %s", seller)
	}
	_, buyer := post(t, fx.handler, `{"glusr_id":"SYNTH-LOOKUP-001","role":"buyer","topic":"violet buyer marker"}`, testToken)
	if !strings.Contains(string(buyer), "synthetic-buyer-only") || strings.Contains(string(buyer), "synthetic-other-seller") {
		t.Fatalf("buyer lookup isolation failed: %s", buyer)
	}
	_, other := post(t, fx.handler, `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic":"orange marker"}`, testToken)
	if !strings.Contains(string(other), `"found":false`) {
		t.Fatalf("different seller data leaked: %s", other)
	}
}

func TestSeparateThreadsAndCorrection(t *testing.T) {
	fx := newFixture(t)
	_, body := post(t, fx.handler, `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic":"pump quantity","thread_id":"pump-enquiry-a","from":"2026-09-01T00:00:00Z","to":"2026-10-01T00:00:00Z","limit":5}`, testToken)
	text := string(body)
	if !strings.Contains(text, "pump-enquiry-a") || !strings.Contains(text, "synthetic-enquiry-a-correction") ||
		!strings.Contains(text, `value=200`) || !strings.Contains(text, "Linked later updates") ||
		strings.Contains(text, "pump-enquiry-b") || strings.Contains(text, `value=500`) {
		t.Fatalf("thread isolation or explicit correction linkage failed: %s", body)
	}
}

func TestSuccessResponseHasExactlyTwoTopLevelFields(t *testing.T) {
	fx := newFixture(t)
	_, body := post(t, fx.handler, `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic":"pump quantity","limit":1}`, testToken)
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("invalid response JSON: %v: %s", err, body)
	}
	if len(payload) != 2 || payload["found"] == nil || payload["context"] == nil {
		t.Fatalf("wanted exactly found and context, got %s", body)
	}
}

func TestRecentDetailOmittedFromContext(t *testing.T) {
	fx := newFixture(t)
	_, body := post(t, fx.handler, `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic_keyword":"blue variant","lookback_days":15}`, testToken)
	if !strings.Contains(string(body), "synthetic-recent-blue") || !strings.Contains(string(body), "Please use the blue pump variant") {
		t.Fatalf("recent omitted detail not found: %s", body)
	}
}

func TestRequestLogsMetadataWithoutPayloadCredentialsOrResponse(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "vani.sqlite")
	seedActualSchema(t, dbPath)
	repository, err := store.OpenReadOnly(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	reference := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	handler := httpapi.New(repository, search.New(repository, 500), testToken, logger, func() time.Time { return reference }).Handler()
	_, _ = post(t, handler, `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic":"confidential blue variant"}`, testToken)
	logText := logs.String()
	for _, required := range []string{`"method":"POST"`, `"path":"/v1/history/lookup"`, `"status":200`, `"duration_ms":`} {
		if !strings.Contains(logText, required) {
			t.Fatalf("missing request log field %s: %s", required, logText)
		}
	}
	for _, forbidden := range []string{testToken, "Authorization", `"found":true`, "Historical evidence:",
		`"msg":"lookup request payload"`, `SYNTH-LOOKUP-001`, `confidential blue variant`} {
		if strings.Contains(logText, forbidden) {
			t.Fatalf("request log leaked %q: %s", forbidden, logText)
		}
	}
}

func TestMalformedPayloadIsNotLogged(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "vani.sqlite")
	seedActualSchema(t, dbPath)
	repository, err := store.OpenReadOnly(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := httpapi.New(repository, search.New(repository, 500), testToken, logger, time.Now).Handler()
	malformed := `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","lookback_days":"ninety"}`
	response, _ := post(t, handler, malformed, testToken)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", response.StatusCode)
	}
	logText := logs.String()
	for _, required := range []string{`"method":"POST"`, `"path":"/v1/history/lookup"`, `"status":400`, `"duration_ms":`} {
		if !strings.Contains(logText, required) {
			t.Fatalf("missing request metadata %s: %s", required, logText)
		}
	}
	for _, forbidden := range []string{testToken, "Authorization", `lookback_days`, `ninety`, `SYNTH-LOOKUP-001`} {
		if strings.Contains(logText, forbidden) {
			t.Fatalf("malformed request payload or credentials leaked %q: %s", forbidden, logText)
		}
	}
}

func TestRejectedExtractionUsesOnlyClearlyMarkedRawTranscriptEvidence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vani.sqlite")
	seedActualSchema(t, dbPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO interactions
(interaction_id,glusr_id,role,channel,source,source_event_id,source_revision,occurred_at,received_at,updated_at,
session_state,summary,important_points_json,source_payload_json,processing_status,processed_revision,last_error)
VALUES ('rejected-extraction','SYNTH-LOOKUP-001','seller','voice','sarvam','rejected-real-shape',2,
'2026-10-08T10:00:00.000Z','2026-10-08T10:01:00.000Z','2026-10-08T10:01:00.000Z','completed',
'STALE TRUSTED SUMMARY MUST NOT APPEAR',
'[{"type":"issue","scope":"thread","thread_id":"stale-thread","field":"other","value":"stale secret","unit":null,"status":"reported","keywords":["stale secret"],"evidence":"STALE POINT MUST NOT APPEAR"}]',
'{"transcript_text":"USER: My invoice is not generated.","enrichment_json":"not-json","validation":{"status":"rejected"}}',
'failed',1,'invalid_enrichment_json')`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	repository, err := store.OpenReadOnly(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := httpapi.New(repository, search.New(repository, 500), testToken, logger,
		func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }).Handler()

	response, body := post(t, handler, `{"glusr_id":"SYNTH-LOOKUP-001","role":"seller","topic_keyword":"invoice generated","lookback_days":15}`, testToken)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"found":true`) ||
		!strings.Contains(string(body), "raw transcript evidence below is not trusted structured memory") ||
		!strings.Contains(string(body), "USER: My invoice is not generated.") {
		t.Fatalf("recoverable raw history was not clearly marked: status=%d body=%s", response.StatusCode, body)
	}
	if strings.Contains(string(body), "STALE TRUSTED SUMMARY") || strings.Contains(string(body), "STALE POINT") ||
		strings.Contains(string(body), "stale secret") {
		t.Fatalf("stale rejected structured content leaked into lookup: %s", body)
	}
}

func stringPointer(value string) *string { return &value }
