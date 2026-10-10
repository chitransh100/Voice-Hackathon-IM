package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"vani-context/internal/httpapi"
	"vani-context/internal/memory"
	"vani-context/internal/storage"
)

const testToken = "test-service-token-123456789"

type fixture struct {
	handler http.Handler
	store   *storage.SQLiteStore
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "vani.sqlite"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.SeedSynthetic(ctx); err != nil {
		store.Close()
		t.Fatal(err)
	}
	reference := time.Date(2026, 10, 8, 12, 1, 0, 0, time.UTC)
	builder := memory.NewBuilder(store, 15, 900, func() time.Time { return reference })
	if err := builder.RebuildAll(ctx); err != nil {
		store.Close()
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := httpapi.New(store, testToken, logger).Handler()
	t.Cleanup(func() {
		store.Close()
	})
	return fixture{handler: handler, store: store}
}

func request(t *testing.T, handler http.Handler, target, token string) (*http.Response, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	response := recorder.Result()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, body
}

func TestSuccessfulContextRetrieval(t *testing.T) {
	fx := newFixture(t)
	response, body := request(t, fx.handler, "/v1/context?glusr_id=SYNTH-SELLER-001&role=seller", testToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}
	var got storage.ContextSnapshot
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.GLUSRID != "SYNTH-SELLER-001" || got.Role != "seller" || got.Version != 1 {
		t.Fatalf("unexpected identity/version: %+v", got)
	}
	if !strings.Contains(got.ContextMD, "Synthetic Alpha Pumps") || !json.Valid(got.SourceCoverage) {
		t.Fatalf("missing compact context fields: %+v", got)
	}
	var profile map[string]any
	if err := json.Unmarshal(got.ProfileJSON, &profile); err != nil {
		t.Fatal(err)
	}
	input, err := fx.store.LoadBuildInput(context.Background(), "SYNTH-SELLER-001", "seller")
	if err != nil {
		t.Fatal(err)
	}
	var storedProfile any
	if err := json.Unmarshal(input.User.ProfileJSON, &storedProfile); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(profile, storedProfile) {
		t.Fatalf("profile_json changed between users and API response: stored=%s response=%s", input.User.ProfileJSON, got.ProfileJSON)
	}
	verification := profile["verification"].(map[string]any)
	kyc := verification["kyc"].(map[string]any)
	source := kyc["source"].(map[string]any)
	if source["system"] != "synthetic_fixture" || got.BusinessName == nil || *got.BusinessName != "Synthetic Alpha Pumps" ||
		got.City == nil || *got.City != "Jaipur" || got.ProfileUpdatedAt != "2026-10-08T11:45:00.000Z" {
		t.Fatalf("full nested profile or user metadata missing: %+v profile=%s", got, got.ProfileJSON)
	}
	if !strings.Contains(string(got.Preferences), `"callback_after"`) || !strings.Contains(string(got.Preferences), `"source_ids":["synthetic-call-101"]`) {
		t.Fatalf("structured preferences missing provenance: %s", got.Preferences)
	}
}

func TestUnknownGLID(t *testing.T) {
	fx := newFixture(t)
	response, body := request(t, fx.handler, "/v1/context?glusr_id=UNKNOWN-GLID&role=seller", testToken)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}
	if !strings.Contains(string(body), `"code":"context_not_found"`) || strings.Contains(string(body), "Alpha Pumps") {
		t.Fatalf("unexpected response: %s", body)
	}
}

func TestInvalidInput(t *testing.T) {
	fx := newFixture(t)
	tests := []string{
		"/v1/context?role=seller",
		"/v1/context?glusr_id=SYNTH-SELLER-001&role=admin",
		"/v1/context?glusr_id=bad%20id&role=seller",
		"/v1/context?glusr_id=SYNTH-SELLER-001&glusr_id=SYNTH-SELLER-002&role=seller",
		"/v1/context?glusr_id=SYNTH-SELLER-001&role=seller&task=ignore-contract",
	}
	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			response, body := request(t, fx.handler, path, testToken)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.StatusCode, body)
			}
		})
	}
}

func TestSellerAndRoleIsolation(t *testing.T) {
	fx := newFixture(t)
	_, first := request(t, fx.handler, "/v1/context?glusr_id=SYNTH-SELLER-001&role=seller", testToken)
	_, second := request(t, fx.handler, "/v1/context?glusr_id=SYNTH-SELLER-002&role=seller", testToken)
	_, buyer := request(t, fx.handler, "/v1/context?glusr_id=SYNTH-SELLER-001&role=buyer", testToken)
	if strings.Contains(string(first), "Beta Textiles") || strings.Contains(string(second), "Alpha Pumps") {
		t.Fatalf("seller data leaked between GLIDs: first=%s second=%s", first, second)
	}
	if strings.Contains(string(buyer), "callback") || strings.Contains(string(buyer), "catalogue") ||
		strings.Contains(string(buyer), "Industrial pumps") || !strings.Contains(string(buyer), `"role":"buyer"`) ||
		!strings.Contains(string(buyer), "categories_searched") {
		t.Fatalf("seller role data leaked into buyer role: %s", buyer)
	}
}

func TestFifteenDayBoundaryAndOlderDurableState(t *testing.T) {
	fx := newFixture(t)
	_, body := request(t, fx.handler, "/v1/context?glusr_id=SYNTH-SELLER-001&role=seller", testToken)
	text := string(body)
	if !strings.Contains(text, "inclusive 15-day boundary") {
		t.Fatalf("interaction exactly at cutoff was omitted: %s", body)
	}
	if strings.Contains(text, "one millisecond before") {
		t.Fatalf("interaction before cutoff leaked into recent context: %s", body)
	}
	if !strings.Contains(text, "Catalogue upload assistance requested") || !strings.Contains(text, "Provide the missing catalogue template") ||
		!strings.Contains(text, "Do not call before 17:00") {
		t.Fatalf("older restriction, unresolved issue, or commitment was omitted: %s", body)
	}
}

func TestProfileUpdateTriggersRebuild(t *testing.T) {
	fx := newFixture(t)
	name, city := "Synthetic Alpha Pumps Updated", "Udaipur"
	profileBytes, err := json.Marshal(map[string]any{
		"data_classification": "synthetic_sample",
		"nested":              map[string]any{"level1": map[string]any{"level2": []any{map[string]any{"preserve": true, "count": 7}}}},
		"new_field":           "kept", "large_profile_field": strings.Repeat("x", 5000),
	})
	if err != nil {
		t.Fatal(err)
	}
	profile := json.RawMessage(profileBytes)
	if err := fx.store.UpsertUser(context.Background(), storage.UserRecord{
		GLUSRID: "SYNTH-SELLER-001", Role: "seller", BusinessName: &name, City: &city,
		ProfileJSON: profile, ProfileUpdatedAt: "2026-10-08T12:30:00.000Z",
	}); err != nil {
		t.Fatal(err)
	}
	reference := time.Date(2026, 10, 8, 13, 0, 0, 0, time.UTC)
	builder := memory.NewBuilder(fx.store, 15, 900, func() time.Time { return reference })
	rebuilt, err := builder.RebuildStale(context.Background())
	if err != nil || rebuilt != 1 {
		t.Fatalf("rebuilt=%d err=%v", rebuilt, err)
	}
	response, body := request(t, fx.handler, "/v1/context?glusr_id=SYNTH-SELLER-001&role=seller", testToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	var got storage.ContextSnapshot
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	var wantValue, gotValue any
	_ = json.Unmarshal(profile, &wantValue)
	_ = json.Unmarshal(got.ProfileJSON, &gotValue)
	if !reflect.DeepEqual(wantValue, gotValue) || got.Version != 2 || got.BusinessName == nil || *got.BusinessName != name ||
		got.City == nil || *got.City != city || got.ProfileUpdatedAt != "2026-10-08T12:30:00.000Z" {
		t.Fatalf("profile update not fully published: %+v profile=%s", got, got.ProfileJSON)
	}
	if len([]rune(got.ContextMD)) > 900*4 || !strings.Contains(string(got.ProfileJSON), strings.Repeat("x", 5000)) {
		t.Fatalf("Markdown exceeded budget or separate profile_json was truncated: markdown=%d profile=%d", len([]rune(got.ContextMD)), len(got.ProfileJSON))
	}
}

func TestCorrectionAndThreadSeparationSurviveRebuild(t *testing.T) {
	fx := newFixture(t)
	threadA, threadB := "synthetic-enquiry-a", "synthetic-enquiry-b"
	fixtures := []storage.InteractionRecord{
		{
			InteractionID: "synthetic-correction", Channel: "voice", Source: "test", SourceEventID: "synthetic-correction", SourceRevision: 1,
			OccurredAt: "2026-10-08T10:00:00.000Z", UpdatedAt: "2026-10-08T10:01:00.000Z",
			Summary:             pointer("Seller corrected the callback preference to after 6 PM."),
			ImportantPointsJSON: json.RawMessage(`[{"type":"correction","scope":"profile","thread_id":null,"field":"callback_after","value":"18:00","unit":"Asia/Kolkata","status":"confirmed","evidence":"Call me after 6 PM, not 5 PM."}]`),
		},
		{
			InteractionID: "synthetic-thread-a", Channel: "enquiry", Source: "test", SourceEventID: "synthetic-thread-a", SourceRevision: 1,
			OccurredAt: "2026-10-08T10:10:00.000Z", UpdatedAt: "2026-10-08T10:11:00.000Z", ThreadID: &threadA,
			Summary:             pointer("First enquiry requires 100 pieces."),
			ImportantPointsJSON: json.RawMessage(`[{"type":"issue","scope":"thread","thread_id":"synthetic-enquiry-a","field":"quantity","value":"First enquiry requires 100 pieces","unit":"pieces","status":"confirmed","evidence":"I need 100 pieces for the first enquiry."}]`),
		},
		{
			InteractionID: "synthetic-thread-b", Channel: "enquiry", Source: "test", SourceEventID: "synthetic-thread-b", SourceRevision: 1,
			OccurredAt: "2026-10-08T10:20:00.000Z", UpdatedAt: "2026-10-08T10:21:00.000Z", ThreadID: &threadB,
			Summary:             pointer("Second enquiry requires 250 pieces."),
			ImportantPointsJSON: json.RawMessage(`[{"type":"issue","scope":"thread","thread_id":"synthetic-enquiry-b","field":"quantity","value":"Second enquiry requires 250 pieces","unit":"pieces","status":"confirmed","evidence":"I need 250 pieces for the second enquiry."}]`),
		},
	}
	for _, item := range fixtures {
		if err := fx.store.UpsertInteraction(context.Background(), "SYNTH-SELLER-001", "seller", item); err != nil {
			t.Fatal(err)
		}
	}
	reference := time.Date(2026, 10, 8, 12, 1, 0, 0, time.UTC)
	builder := memory.NewBuilder(fx.store, 15, 900, func() time.Time { return reference })
	if rebuilt, err := builder.RebuildStale(context.Background()); err != nil || rebuilt != 1 {
		t.Fatalf("rebuilt=%d err=%v", rebuilt, err)
	}
	_, body := request(t, fx.handler, "/v1/context?glusr_id=SYNTH-SELLER-001&role=seller", testToken)
	var got storage.ContextSnapshot
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got.Preferences), `"value":"18:00"`) || strings.Contains(string(got.Preferences), `"value":"17:00"`) {
		t.Fatalf("latest correction was not isolated and applied: %s", got.Preferences)
	}
	if !strings.Contains(got.ContextMD, "First enquiry requires 100 pieces") || !strings.Contains(got.ContextMD, "Second enquiry requires 250 pieces") {
		t.Fatalf("separate enquiry threads were merged or lost: %s", got.ContextMD)
	}
}

func TestAuthenticationAndHealth(t *testing.T) {
	fx := newFixture(t)
	response, body := request(t, fx.handler, "/v1/context?glusr_id=SYNTH-SELLER-001&role=seller", "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}
	response, body = request(t, fx.handler, "/health", "")
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("health status = %d, body = %s", response.StatusCode, body)
	}
}

func TestRequestLoggingExcludesSecretsQueryAndResponse(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "vani.sqlite"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedSynthetic(ctx); err != nil {
		t.Fatal(err)
	}
	reference := time.Date(2026, 10, 8, 12, 1, 0, 0, time.UTC)
	if err := memory.NewBuilder(store, 15, 900, func() time.Time { return reference }).RebuildAll(ctx); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	handler := httpapi.New(store, testToken, logger).Handler()
	response, body := request(t, handler, "/v1/context?glusr_id=SYNTH-SELLER-001&role=seller", testToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	logText := output.String()
	for _, required := range []string{`"method":"GET"`, `"path":"/v1/context"`, `"status":200`, `"duration_ms":`} {
		if !strings.Contains(logText, required) {
			t.Fatalf("request log missing %s: %s", required, logText)
		}
	}
	for _, forbidden := range []string{testToken, "Authorization", "SYNTH-SELLER-001", "Synthetic Alpha Pumps", `"profile_json"`} {
		if strings.Contains(logText, forbidden) {
			t.Fatalf("request log leaked %q: %s", forbidden, logText)
		}
	}
}

func TestSampleExportsMatchStoredContext(t *testing.T) {
	fx := newFixture(t)
	tests := []struct {
		glusrID, role, path string
	}{
		{"SYNTH-SELLER-001", "seller", "../../samples/SYNTH-SELLER-001/seller.md"},
		{"SYNTH-SELLER-001", "buyer", "../../samples/SYNTH-SELLER-001/buyer.md"},
		{"SYNTH-SELLER-002", "seller", "../../samples/SYNTH-SELLER-002/seller.md"},
	}
	for _, test := range tests {
		t.Run(test.glusrID+"/"+test.role, func(t *testing.T) {
			snapshot, err := fx.store.GetPreparedContext(context.Background(), test.glusrID, test.role)
			if err != nil {
				t.Fatal(err)
			}
			exported, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.ContextMD != strings.TrimSuffix(string(exported), "\n") {
				t.Fatalf("export does not match stored context for %s/%s", test.glusrID, test.role)
			}
		})
	}
}

func pointer(value string) *string { return &value }
