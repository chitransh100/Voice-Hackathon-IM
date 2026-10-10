package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"vani-context/enrichmentapi/shared"
	contextstorage "vani-context/internal/storage"
	"vani-context/profileapi/internal/httpapi"
)

const token = "profile-test-token-123456"

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vani.sqlite")
	ctx := context.Background()
	store, err := contextstorage.Open(ctx, path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	business, city := "Complete Nested Company", "Pune"
	for _, item := range []struct {
		role    string
		profile json.RawMessage
	}{
		{"seller", json.RawMessage(`{"categories":[{"id":"A","details":{"products":["one","two"]}}],"verification":{"kyc":{"status":"verified","source":{"system":"test","checks":[1,2,3]}}},"custom":{"nested":{"preserved":true}}}`)},
		{"buyer", json.RawMessage(`{"buyer_profile":{"categories_searched":["motors"],"private_marker":"buyer-only"}}`)},
	} {
		if err := store.UpsertUser(ctx, contextstorage.UserRecord{GLUSRID: "SYNTH-PROFILE-001", Role: item.role,
			BusinessName: &business, City: &city, ProfileJSON: item.profile, ProfileUpdatedAt: "2026-10-09T00:00:00.000Z"}); err != nil {
			t.Fatal(err)
		}
	}
	db, err := shared.OpenSQLite(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return httpapi.New(db, token, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
}

func get(handler http.Handler, target, auth string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if auth != "" {
		request.Header.Set("Authorization", "Bearer "+auth)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestCompleteNestedProfileAndRoleIsolation(t *testing.T) {
	handler := newHandler(t)
	seller := get(handler, "/users/SYNTH-PROFILE-001/profile?role=seller", token)
	if seller.Code != 200 {
		t.Fatalf("status=%d body=%s", seller.Code, seller.Body.Bytes())
	}
	var payload map[string]any
	if err := json.Unmarshal(seller.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	profile := payload["profile_json"].(map[string]any)
	if profile["custom"].(map[string]any)["nested"].(map[string]any)["preserved"] != true || profile["verification"] == nil {
		t.Fatalf("nested profile not preserved: %s", seller.Body.Bytes())
	}
	if _, leaked := profile["buyer_profile"]; leaked {
		t.Fatalf("buyer profile leaked: %s", seller.Body.Bytes())
	}
	buyer := get(handler, "/users/SYNTH-PROFILE-001/profile?role=buyer", token)
	if buyer.Code != 200 || !contains(buyer.Body.Bytes(), "buyer-only") || contains(buyer.Body.Bytes(), "verified") {
		t.Fatalf("buyer isolation failed: status=%d body=%s", buyer.Code, buyer.Body.Bytes())
	}
}

func TestProfileErrors(t *testing.T) {
	handler := newHandler(t)
	for _, test := range []struct {
		target, auth string
		status       int
	}{
		{"/users/UNKNOWN/profile?role=seller", token, 404},
		{"/users/SYNTH-PROFILE-001/profile?role=admin", token, 400},
		{"/users/bad%20id/profile?role=seller", token, 400},
		{"/users/SYNTH-PROFILE-001/profile?role=seller&extra=1", token, 400},
		{"/users/SYNTH-PROFILE-001/profile?role=seller", "", 401},
	} {
		response := get(handler, test.target, test.auth)
		if response.Code != test.status {
			t.Fatalf("target=%s status=%d body=%s", test.target, response.Code, response.Body.Bytes())
		}
	}
}

func contains(value []byte, text string) bool {
	for index := 0; index+len(text) <= len(value); index++ {
		if string(value[index:index+len(text)]) == text {
			return true
		}
	}
	return false
}
