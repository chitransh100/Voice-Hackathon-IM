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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"vani-context/enrichmentapi/internal/enrich"
	"vani-context/enrichmentapi/internal/httpapi"
	"vani-context/enrichmentapi/shared"
	"vani-context/internal/memory"
	contextstorage "vani-context/internal/storage"
)

const testToken = "enrichment-test-token-12345"

type fixture struct {
	handler      http.Handler
	db           *sql.DB
	contextStore *contextstorage.SQLiteStore
	now          time.Time
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vani.sqlite")
	ctx := context.Background()
	contextStore, err := contextstorage.Open(ctx, path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { contextStore.Close() })
	if err := contextStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	company, city := "Nested Profile Company", "Jaipur"
	profile := json.RawMessage(`{"data_classification":"synthetic_sample","categories":[{"id":"PUMP","details":{"grade":"A"}}],"verification":{"kyc":{"status":"verified","source":{"system":"fixture"}}},"unrelated":{"keep":true}}`)
	for _, role := range []string{"seller", "buyer"} {
		if err := contextStore.UpsertUser(ctx, contextstorage.UserRecord{GLUSRID: "SYNTH-ENRICH-001", Role: role,
			BusinessName: &company, City: &city, ProfileJSON: profile, ProfileUpdatedAt: "2026-10-01T00:00:00.000Z"}); err != nil {
			t.Fatal(err)
		}
	}
	db, err := shared.OpenSQLite(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reference := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 9, 12, 1, 0, 0, time.UTC)
	builder := memory.NewBuilder(contextStore, 15, 900, func() time.Time { return reference })
	service := enrich.NewService(db, builder, func() time.Time { return now })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ready := func(ctx context.Context) error { var one int; return db.QueryRowContext(ctx, "SELECT 1").Scan(&one) }
	workflowTimezone, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	return fixture{handler: httpapi.New(service, testToken, logger, ready, workflowTimezone).Handler(), db: db, contextStore: contextStore, now: now}
}

func validRequest(eventID string, occurred time.Time) map[string]any {
	points := []map[string]any{
		{"type": "preference", "scope": "profile", "thread_id": nil, "field": "other", "value": "Hindi", "unit": nil,
			"status": "confirmed", "keywords": []string{"language", "preferred language"}, "evidence": "Please speak with me in Hindi."},
		{"type": "correction", "scope": "profile", "thread_id": nil, "field": "callback_after", "value": "18:00", "unit": "Asia/Kolkata",
			"status": "confirmed", "keywords": []string{"callback", "call time"}, "evidence": "Call me after 18:00."},
	}
	pointsJSON, _ := json.Marshal(points)
	inner := map[string]any{
		"summary":               "Seller selected Hindi and changed the lasting callback preference to after 18:00.",
		"important_points_json": string(pointsJSON),
		"profile_updates": []map[string]any{
			{"path": "preferences.preferred_language", "value": "Hindi", "evidence_point_index": 0},
			{"path": "preferences.callback_after", "value": "18:00", "evidence_point_index": 1},
		},
	}
	innerJSON, _ := json.Marshal(inner)
	return map[string]any{
		"glusr_id": "SYNTH-ENRICH-001", "role": "seller", "source": "sarvam", "source_event_id": eventID,
		"channel": "voice", "occurred_at": occurred.Format(time.RFC3339Nano), "session_state": "completed",
		"thread_id": "language-call-1", "transcript": "USER: Please speak with me in Hindi.\nUSER: Call me after 18:00.",
		"enrichment_json": string(innerJSON),
	}
}

func post(t *testing.T, handler http.Handler, request map[string]any) (int, []byte) {
	t.Helper()
	body, _ := json.Marshal(request)
	httpRequest := httptest.NewRequest(http.MethodPost, "/interactions/enrich", bytes.NewReader(body))
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+testToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httpRequest)
	return recorder.Code, recorder.Body.Bytes()
}

func TestEnrichProfileContextAndDuplicate(t *testing.T) {
	fx := newFixture(t)
	request := validRequest("call-001", time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC))
	status, body := post(t, fx.handler, request)
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var first enrich.Response
	if err := json.Unmarshal(body, &first); err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || first.ProcessingStatus != "processed" || first.ContextVersion == nil || *first.ContextVersion != 1 || len(first.AppliedChanges) != 2 {
		t.Fatalf("unexpected first response: %s", body)
	}
	var profileRaw string
	if err := fx.db.QueryRow(`SELECT profile_json FROM users WHERE glusr_id=? AND role='seller'`, "SYNTH-ENRICH-001").Scan(&profileRaw); err != nil {
		t.Fatal(err)
	}
	var profile map[string]any
	if err := json.Unmarshal([]byte(profileRaw), &profile); err != nil {
		t.Fatal(err)
	}
	preferences := profile["preferences"].(map[string]any)
	if preferences["preferred_language"] != "Hindi" || preferences["callback_after"] != "18:00" {
		t.Fatalf("profile changes missing: %s", profileRaw)
	}
	if profile["unrelated"].(map[string]any)["keep"] != true || profile["verification"].(map[string]any)["kyc"] == nil {
		t.Fatalf("unrelated nested profile was not preserved: %s", profileRaw)
	}
	var memoryRaw string
	if err := fx.db.QueryRow(`SELECT memory_json FROM current_context WHERE glusr_id=? AND role='seller'`, "SYNTH-ENRICH-001").Scan(&memoryRaw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains([]byte(memoryRaw), []byte(`"preferred_language":"Hindi"`)) || !bytes.Contains([]byte(memoryRaw), []byte(`"callback_after":"18:00"`)) {
		t.Fatalf("context snapshot lacks enriched profile: %s", memoryRaw)
	}

	status, body = post(t, fx.handler, request)
	if status != 200 {
		t.Fatalf("duplicate status=%d body=%s", status, body)
	}
	var duplicate enrich.Response
	_ = json.Unmarshal(body, &duplicate)
	if !duplicate.Duplicate || duplicate.AcceptedRevision != 1 {
		t.Fatalf("duplicate not detected: %s", body)
	}
	var version, count int
	_ = fx.db.QueryRow(`SELECT version FROM current_context WHERE glusr_id=? AND role='seller'`, "SYNTH-ENRICH-001").Scan(&version)
	_ = fx.db.QueryRow(`SELECT COUNT(*) FROM interactions WHERE glusr_id=? AND role='seller'`, "SYNTH-ENRICH-001").Scan(&count)
	if version != 1 || count != 1 {
		t.Fatalf("duplicate mutated state: version=%d count=%d", version, count)
	}
}

func TestSarvamStartDatetimeUsesConfiguredWorkflowTimezone(t *testing.T) {
	fx := newFixture(t)
	request := validRequest("call-sarvam-start-time", time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC))
	delete(request, "occurred_at")
	request["start_datetime"] = "2026-10-09 15:30:00"

	status, body := post(t, fx.handler, request)
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var occurredAt, payloadRaw string
	if err := fx.db.QueryRow(`SELECT occurred_at, source_payload_json FROM interactions
WHERE source='sarvam' AND source_event_id=?`, "call-sarvam-start-time").Scan(&occurredAt, &payloadRaw); err != nil {
		t.Fatal(err)
	}
	if occurredAt != "2026-10-09T10:00:00.000Z" {
		t.Fatalf("timezone adapter produced %q", occurredAt)
	}
	var payload struct {
		EventTime enrich.EventTimeMetadata `json:"event_time"`
	}
	if err := json.Unmarshal([]byte(payloadRaw), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.EventTime.InputField != "start_datetime" || payload.EventTime.RawValue != "2026-10-09 15:30:00" ||
		payload.EventTime.SourceTimezone != "Asia/Kolkata" || payload.EventTime.NormalizedUTC != occurredAt {
		t.Fatalf("unexpected event-time provenance: %+v", payload.EventTime)
	}

	both := validRequest("call-both-times", time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC))
	both["start_datetime"] = "2026-10-09 15:30:00"
	if status, _ := post(t, fx.handler, both); status != 400 {
		t.Fatalf("both event-time fields status=%d", status)
	}
	malformed := validRequest("call-bad-start-time", time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC))
	delete(malformed, "occurred_at")
	malformed["start_datetime"] = "2026/10/09 15:30:00"
	if status, _ := post(t, fx.handler, malformed); status != 400 {
		t.Fatalf("malformed start_datetime status=%d", status)
	}
}

func TestSarvamStructuredTranscriptObjectEnrichmentAndSlashEventID(t *testing.T) {
	fx := newFixture(t)
	request := validRequest("20261010/structured-call-00:25:26-abc", time.Date(2026, 10, 9, 18, 55, 0, 0, time.UTC))
	request["source"] = " Sarwam Voice Platform "
	request["channel"] = "Voice"
	delete(request, "occurred_at")
	request["start_datetime"] = "2026-10-10T00:33:43"
	request["transcript"] = []map[string]any{
		{"role": "agent", "en_text": "Which language should I use?", "indic_text": nil},
		{"role": "user", "en_text": "Please speak with me in Hindi.", "indic_text": "कृपया मुझसे हिंदी में बात करें।"},
		{"role": "user", "en_text": "Call me after 18:00.", "indic_text": nil},
	}
	var enrichmentObject map[string]any
	if err := json.Unmarshal([]byte(request["enrichment_json"].(string)), &enrichmentObject); err != nil {
		t.Fatal(err)
	}
	request["enrichment_json"] = enrichmentObject

	status, body := post(t, fx.handler, request)
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var source, channel, occurredAt, payloadRaw string
	if err := fx.db.QueryRow(`SELECT source,channel,occurred_at,source_payload_json FROM interactions WHERE source_event_id=?`,
		"20261010/structured-call-00:25:26-abc").Scan(&source, &channel, &occurredAt, &payloadRaw); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Transcript   string                      `json:"transcript_text"`
		InputAdapter enrich.InputAdapterMetadata `json:"input_adapter"`
	}
	if err := json.Unmarshal([]byte(payloadRaw), &payload); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"AGENT: Which language should I use?", "USER: Please speak with me in Hindi.",
		"USER: कृपया मुझसे हिंदी में बात करें।", "USER: Call me after 18:00."} {
		if !strings.Contains(payload.Transcript, expected) {
			t.Fatalf("normalized transcript lacks %q: %s", expected, payload.Transcript)
		}
	}
	if source != "sarwam_voice_platform" || channel != "voice" || occurredAt != "2026-10-09T19:03:43.000Z" {
		t.Fatalf("unexpected canonical input: source=%q channel=%q occurred_at=%q", source, channel, occurredAt)
	}
	if payload.InputAdapter.OriginalSource != " Sarwam Voice Platform " || payload.InputAdapter.OriginalChannel != "Voice" ||
		payload.InputAdapter.CanonicalSource != source || payload.InputAdapter.CanonicalChannel != channel {
		t.Fatalf("unexpected input adapter provenance: %+v", payload.InputAdapter)
	}

	unknownChannel := validRequest("external-channel-001", time.Date(2026, 10, 9, 19, 5, 0, 0, time.UTC))
	unknownChannel["source"] = "External CRM"
	unknownChannel["channel"] = "Instagram DM"
	if status, body := post(t, fx.handler, unknownChannel); status != 200 {
		t.Fatalf("unknown channel status=%d body=%s", status, body)
	}
	var storedChannel string
	if err := fx.db.QueryRow(`SELECT channel FROM interactions WHERE source_event_id='external-channel-001'`).Scan(&storedChannel); err != nil {
		t.Fatal(err)
	}
	if storedChannel != "other" {
		t.Fatalf("unknown channel stored as %q", storedChannel)
	}
}

func TestValidationSkippedAndAuthentication(t *testing.T) {
	fx := newFixture(t)
	base := validRequest("call-validation", time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC))

	malformed := cloneMap(base)
	malformed["enrichment_json"] = "not-json"
	if status, body := post(t, fx.handler, malformed); status != 202 || !bytes.Contains(body, []byte(`"extraction_status":"rejected"`)) {
		t.Fatalf("malformed status=%d", status)
	}

	invalidEnum := cloneMap(base)
	var inner map[string]any
	_ = json.Unmarshal([]byte(invalidEnum["enrichment_json"].(string)), &inner)
	var points []map[string]any
	_ = json.Unmarshal([]byte(inner["important_points_json"].(string)), &points)
	points[0]["type"] = "personality"
	encodedPoints, _ := json.Marshal(points)
	inner["important_points_json"] = string(encodedPoints)
	encodedInner, _ := json.Marshal(inner)
	invalidEnum["enrichment_json"] = string(encodedInner)
	if status, body := post(t, fx.handler, invalidEnum); status != 202 || !bytes.Contains(body, []byte(`"extraction_status":"rejected"`)) {
		t.Fatalf("invalid enum status=%d", status)
	}

	unsupported := validRequest("call-skipped", time.Date(2026, 10, 9, 10, 5, 0, 0, time.UTC))
	_ = json.Unmarshal([]byte(unsupported["enrichment_json"].(string)), &inner)
	inner["profile_updates"] = []map[string]any{
		{"path": "verification.kyc.status", "value": "Hindi", "evidence_point_index": 0},
		{"path": "preferences.timezone", "value": "Asia/Kolkata", "evidence_point_index": 99},
	}
	encodedInner, _ = json.Marshal(inner)
	unsupported["enrichment_json"] = string(encodedInner)
	status, body := post(t, fx.handler, unsupported)
	if status != 200 || !bytes.Contains(body, []byte("unsupported_path")) || !bytes.Contains(body, []byte("missing_evidence_point")) {
		t.Fatalf("unsupported/missing evidence response: status=%d body=%s", status, body)
	}

	requestBody, _ := json.Marshal(base)
	request := httptest.NewRequest(http.MethodPost, "/interactions/enrich", bytes.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	fx.handler.ServeHTTP(recorder, request)
	if recorder.Code != 401 {
		t.Fatalf("unauthenticated status=%d", recorder.Code)
	}
}

func TestUnsupportedEvidenceIsPartiallyAcceptedAndPreserved(t *testing.T) {
	fx := newFixture(t)
	request := validRequest("call-unsupported-evidence", time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC))
	var inner map[string]any
	if err := json.Unmarshal([]byte(request["enrichment_json"].(string)), &inner); err != nil {
		t.Fatal(err)
	}
	var points []map[string]any
	if err := json.Unmarshal([]byte(inner["important_points_json"].(string)), &points); err != nil {
		t.Fatal(err)
	}
	points[0]["evidence"] = "Please use Hindi in every future conversation."
	encodedPoints, _ := json.Marshal(points[:1])
	inner["important_points_json"] = string(encodedPoints)
	inner["summary"] = "Model-generated summary containing an unsupported language claim."
	inner["profile_updates"] = []map[string]any{{
		"path": "preferences.preferred_language", "value": "Hindi", "evidence_point_index": 0,
	}}
	encodedInner, _ := json.Marshal(inner)
	request["enrichment_json"] = string(encodedInner)

	status, body := post(t, fx.handler, request)
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var response enrich.Response
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.ValidationWarnings) != 1 || response.ValidationWarnings[0].PointIndex != 0 ||
		response.ValidationWarnings[0].Code != "unsupported_evidence" {
		t.Fatalf("missing indexed evidence warning: %s", body)
	}
	if len(response.AppliedChanges) != 0 || len(response.SkippedChanges) != 1 ||
		response.SkippedChanges[0].EvidencePointIndex != 0 ||
		response.SkippedChanges[0].Reason != "referenced_point_has_unsupported_evidence" {
		t.Fatalf("dependent update was not skipped: %s", body)
	}

	var summary, trustedPoints, payloadRaw, profileRaw string
	if err := fx.db.QueryRow(`SELECT summary,important_points_json,source_payload_json FROM interactions
WHERE source_event_id=?`, "call-unsupported-evidence").Scan(&summary, &trustedPoints, &payloadRaw); err != nil {
		t.Fatal(err)
	}
	if err := fx.db.QueryRow(`SELECT profile_json FROM users WHERE glusr_id=? AND role='seller'`, "SYNTH-ENRICH-001").Scan(&profileRaw); err != nil {
		t.Fatal(err)
	}
	if summary != inner["summary"] || trustedPoints != "[]" || strings.Contains(profileRaw, "preferred_language") {
		t.Fatalf("untrusted data escaped filtering: summary=%q points=%s profile=%s", summary, trustedPoints, profileRaw)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadRaw), &payload); err != nil {
		t.Fatal(err)
	}
	validation := payload["validation"].(map[string]any)
	if payload["transcript_text"] != request["transcript"] || payload["enrichment_json"] != request["enrichment_json"] ||
		validation["summary_provenance"] != "model_generated" || validation["submitted_point_count"] != float64(1) ||
		validation["trusted_point_count"] != float64(0) {
		t.Fatalf("submission provenance was not preserved: %s", payloadRaw)
	}
}

func TestMixedEvidenceKeepsOriginalIndicesAndCorrectedRevision(t *testing.T) {
	fx := newFixture(t)
	request := validRequest("call-mixed-evidence", time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC))
	var inner map[string]any
	_ = json.Unmarshal([]byte(request["enrichment_json"].(string)), &inner)
	var points []map[string]any
	_ = json.Unmarshal([]byte(inner["important_points_json"].(string)), &points)
	points[0]["evidence"] = "Use Hindi in all future conversations."
	encodedPoints, _ := json.Marshal(points)
	inner["important_points_json"] = string(encodedPoints)
	encodedInner, _ := json.Marshal(inner)
	request["enrichment_json"] = string(encodedInner)

	status, body := post(t, fx.handler, request)
	if status != 200 {
		t.Fatalf("mixed status=%d body=%s", status, body)
	}
	var first enrich.Response
	_ = json.Unmarshal(body, &first)
	if len(first.ValidationWarnings) != 1 || len(first.AppliedChanges) != 1 || len(first.SkippedChanges) != 1 ||
		first.AppliedChanges[0].Path != "preferences.callback_after" || first.AppliedChanges[0].EvidencePointIndex != 1 ||
		first.SkippedChanges[0].Path != "preferences.preferred_language" || first.SkippedChanges[0].EvidencePointIndex != 0 {
		t.Fatalf("mixed point isolation failed: %s", body)
	}
	var trustedRaw string
	_ = fx.db.QueryRow(`SELECT important_points_json FROM interactions WHERE source_event_id=?`, "call-mixed-evidence").Scan(&trustedRaw)
	var trusted []enrich.Point
	if err := json.Unmarshal([]byte(trustedRaw), &trusted); err != nil || len(trusted) != 1 || trusted[0].Field != "callback_after" {
		t.Fatalf("trusted points contain invalid evidence: %s", trustedRaw)
	}

	corrected := cloneMap(request)
	_ = json.Unmarshal([]byte(corrected["enrichment_json"].(string)), &inner)
	_ = json.Unmarshal([]byte(inner["important_points_json"].(string)), &points)
	points[0]["evidence"] = "Please speak with me in Hindi."
	encodedPoints, _ = json.Marshal(points)
	inner["important_points_json"] = string(encodedPoints)
	encodedInner, _ = json.Marshal(inner)
	corrected["enrichment_json"] = string(encodedInner)
	status, body = post(t, fx.handler, corrected)
	if status != 200 {
		t.Fatalf("corrected status=%d body=%s", status, body)
	}
	var revised enrich.Response
	_ = json.Unmarshal(body, &revised)
	if revised.AcceptedRevision != 2 || revised.Duplicate || len(revised.ValidationWarnings) != 0 ||
		len(revised.AppliedChanges) != 1 || revised.AppliedChanges[0].Path != "preferences.preferred_language" ||
		revised.AppliedChanges[0].EvidencePointIndex != 0 {
		t.Fatalf("corrected revision failed: %s", body)
	}
	var revisedPayloadRaw string
	if err := fx.db.QueryRow(`SELECT source_payload_json FROM interactions WHERE source_event_id=?`, "call-mixed-evidence").Scan(&revisedPayloadRaw); err != nil {
		t.Fatal(err)
	}
	var revisedPayload struct {
		RevisionHistory []struct {
			Revision   int `json:"revision"`
			Validation struct {
				Warnings []enrich.ValidationWarning `json:"warnings"`
			} `json:"validation"`
		} `json:"revision_history"`
	}
	if err := json.Unmarshal([]byte(revisedPayloadRaw), &revisedPayload); err != nil || len(revisedPayload.RevisionHistory) != 1 ||
		revisedPayload.RevisionHistory[0].Revision != 1 || len(revisedPayload.RevisionHistory[0].Validation.Warnings) != 1 ||
		revisedPayload.RevisionHistory[0].Validation.Warnings[0].PointIndex != 0 {
		t.Fatalf("corrected revision lost earlier validation provenance: %s", revisedPayloadRaw)
	}
	status, body = post(t, fx.handler, corrected)
	if status != 200 {
		t.Fatalf("corrected duplicate status=%d body=%s", status, body)
	}
	var duplicate enrich.Response
	_ = json.Unmarshal(body, &duplicate)
	if !duplicate.Duplicate || duplicate.AcceptedRevision != 2 || len(duplicate.ValidationWarnings) != 0 {
		t.Fatalf("corrected retry was not idempotent: %s", body)
	}
}

func TestRoleIsolationConflictAndChangedRevision(t *testing.T) {
	fx := newFixture(t)
	newer := validRequest("newer-language", time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC))
	if status, body := post(t, fx.handler, newer); status != 200 {
		t.Fatalf("newer status=%d body=%s", status, body)
	}
	older := languageRequest("older-language", "English", time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC))
	status, body := post(t, fx.handler, older)
	if status != 200 || !bytes.Contains(body, []byte("newer_profile_change_exists")) {
		t.Fatalf("conflict not reported: status=%d body=%s", status, body)
	}
	var sellerProfile, buyerProfile string
	_ = fx.db.QueryRow(`SELECT profile_json FROM users WHERE glusr_id=? AND role='seller'`, "SYNTH-ENRICH-001").Scan(&sellerProfile)
	_ = fx.db.QueryRow(`SELECT profile_json FROM users WHERE glusr_id=? AND role='buyer'`, "SYNTH-ENRICH-001").Scan(&buyerProfile)
	if !bytes.Contains([]byte(sellerProfile), []byte(`"preferred_language":"Hindi"`)) || bytes.Contains([]byte(buyerProfile), []byte("preferred_language")) {
		t.Fatalf("role isolation failed seller=%s buyer=%s", sellerProfile, buyerProfile)
	}

	changed := cloneMap(newer)
	var inner map[string]any
	_ = json.Unmarshal([]byte(changed["enrichment_json"].(string)), &inner)
	inner["summary"] = "Changed final summary for the same source event."
	encoded, _ := json.Marshal(inner)
	changed["enrichment_json"] = string(encoded)
	status, body = post(t, fx.handler, changed)
	if status != 200 {
		t.Fatalf("changed revision status=%d body=%s", status, body)
	}
	var response enrich.Response
	_ = json.Unmarshal(body, &response)
	if response.AcceptedRevision != 2 || response.Duplicate {
		t.Fatalf("changed payload did not create revision 2: %s", body)
	}
}

func TestContextBuildFailureAndDuplicateRecovery(t *testing.T) {
	fx := newFixture(t)
	_, err := fx.db.Exec(`INSERT INTO interactions
(interaction_id,glusr_id,role,channel,source,source_event_id,source_revision,occurred_at,received_at,updated_at,
session_state,summary,important_points_json,source_payload_json,processing_status,processed_revision)
VALUES ('broken-row','SYNTH-ENRICH-001','seller','voice','fixture','broken',1,
'2026-10-08T00:00:00.000Z','2026-10-08T00:00:00.000Z','2026-10-08T00:00:00.000Z',
'completed','Broken fixture','not-json','{}','processed',1)`)
	if err != nil {
		t.Fatal(err)
	}
	request := languageRequest("recovery-call", "Hindi", time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC))
	status, body := post(t, fx.handler, request)
	if status != 202 || !bytes.Contains(body, []byte(`"processing_status":"failed"`)) {
		t.Fatalf("build failure not retained: status=%d body=%s", status, body)
	}
	if _, err := fx.db.Exec(`UPDATE interactions SET important_points_json='[]' WHERE interaction_id='broken-row'`); err != nil {
		t.Fatal(err)
	}
	status, body = post(t, fx.handler, request)
	if status != 200 || !bytes.Contains(body, []byte(`"duplicate":true`)) || !bytes.Contains(body, []byte(`"processing_status":"processed"`)) {
		t.Fatalf("duplicate recovery failed: status=%d body=%s", status, body)
	}
}

func TestMalformedExtractionIsDurableAndRecoverableByRevision(t *testing.T) {
	fx := newFixture(t)
	request := validRequest("call-malformed-recoverable", time.Date(2026, 10, 9, 10, 20, 0, 0, time.UTC))
	request["enrichment_json"] = `{"summary":"first object","important_points_json":"[]","profile_updates":[]} {"second":true}`

	status, body := post(t, fx.handler, request)
	if status != http.StatusAccepted || !bytes.Contains(body, []byte(`"extraction_status":"rejected"`)) ||
		!bytes.Contains(body, []byte(`"code":"invalid_enrichment_json"`)) {
		t.Fatalf("malformed extraction was not durably rejected: status=%d body=%s", status, body)
	}
	var interactionID, processingStatus, lastError, payloadRaw string
	var summary sql.NullString
	var sourceRevision, processedRevision int
	if err := fx.db.QueryRow(`SELECT interaction_id,source_revision,processed_revision,processing_status,last_error,summary,source_payload_json
FROM interactions WHERE source_event_id=?`, "call-malformed-recoverable").
		Scan(&interactionID, &sourceRevision, &processedRevision, &processingStatus, &lastError, &summary, &payloadRaw); err != nil {
		t.Fatal(err)
	}
	var rejectedPayload struct {
		Transcript     string `json:"transcript_text"`
		EnrichmentJSON string `json:"enrichment_json"`
	}
	if err := json.Unmarshal([]byte(payloadRaw), &rejectedPayload); err != nil {
		t.Fatal(err)
	}
	if interactionID == "" || sourceRevision != 1 || processedRevision != 0 || processingStatus != "failed" ||
		lastError != "invalid_enrichment_json" || summary.Valid || !strings.Contains(rejectedPayload.EnrichmentJSON, `{"second":true}`) ||
		!strings.Contains(rejectedPayload.Transcript, `USER: Please speak with me in Hindi.`) {
		t.Fatalf("raw rejection provenance incomplete: revision=%d processed=%d status=%s error=%s summary=%v payload=%s",
			sourceRevision, processedRevision, processingStatus, lastError, summary, payloadRaw)
	}

	corrected := validRequest("call-malformed-recoverable", time.Date(2026, 10, 9, 10, 20, 0, 0, time.UTC))
	status, body = post(t, fx.handler, corrected)
	if status != http.StatusOK {
		t.Fatalf("corrected revision status=%d body=%s", status, body)
	}
	var response enrich.Response
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if response.AcceptedRevision != 2 || response.Duplicate || response.ProcessingStatus != "processed" {
		t.Fatalf("corrected extraction was not revision 2: %s", body)
	}
	var count int
	_ = fx.db.QueryRow(`SELECT COUNT(*) FROM interactions WHERE source_event_id=?`, "call-malformed-recoverable").Scan(&count)
	if count != 1 {
		t.Fatalf("correction duplicated interaction: count=%d", count)
	}
	if err := fx.db.QueryRow(`SELECT source_payload_json FROM interactions WHERE source_event_id=?`, "call-malformed-recoverable").Scan(&payloadRaw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payloadRaw, `"revision":1`) || !strings.Contains(payloadRaw, `"status":"rejected"`) ||
		!strings.Contains(payloadRaw, `"status":"accepted"`) {
		t.Fatalf("revision history did not retain rejected extraction: %s", payloadRaw)
	}
}

func TestSpeakerAwareHindiEvidenceAndThreadLifecycle(t *testing.T) {
	fx := newFixture(t)
	issue := extractionRequest("call-hindi-new-issue", time.Date(2026, 10, 9, 10, 25, 0, 0, time.UTC),
		[]map[string]any{
			{"role": "agent", "en_text": "Is login working?", "indic_text": "क्या लॉगिन काम कर रहा है?"},
			{"role": "user", "en_text": "My login is still not working.", "indic_text": "मेरा लॉगिन अभी भी काम नहीं कर रहा है।"},
		}, "Seller reported a login issue.", []map[string]any{{
			"type": "issue", "scope": "thread", "thread_id": nil, "field": "other", "value": "login not working", "unit": nil,
			"status": "reported", "keywords": []string{"login issue", "login not working"}, "evidence": "मेरा लॉगिन अभी भी काम नहीं कर रहा है।",
		}}, []map[string]any{})
	status, body := post(t, fx.handler, issue)
	if status != http.StatusOK {
		t.Fatalf("Hindi issue status=%d body=%s", status, body)
	}
	var pointsRaw, payloadRaw string
	if err := fx.db.QueryRow(`SELECT important_points_json,source_payload_json FROM interactions WHERE source_event_id=?`, "call-hindi-new-issue").Scan(&pointsRaw, &payloadRaw); err != nil {
		t.Fatal(err)
	}
	var points []enrich.Point
	if err := json.Unmarshal([]byte(pointsRaw), &points); err != nil || len(points) != 1 || points[0].ThreadID == nil {
		t.Fatalf("new issue did not receive a thread ID: %s", pointsRaw)
	}
	threadID := *points[0].ThreadID
	if !strings.Contains(payloadRaw, `"kind":"en_text"`) || !strings.Contains(payloadRaw, `"kind":"indic_text"`) ||
		!strings.Contains(payloadRaw, "मेरा लॉगिन अभी भी काम नहीं कर रहा है।") {
		t.Fatalf("original and translated transcript variants were not retained: %s", payloadRaw)
	}
	assertOpenThread(t, fx.db, threadID, true)

	missing := extractionRequest("call-hindi-resolution-missing-thread", time.Date(2026, 10, 9, 10, 30, 0, 0, time.UTC),
		[]map[string]any{{"role": "user", "en_text": "My login issue is resolved.", "indic_text": "मेरा लॉगिन अब ठीक हो गया है।"}},
		"Seller said login was resolved.", []map[string]any{{
			"type": "resolution", "scope": "thread", "thread_id": nil, "field": "other", "value": "resolved", "unit": nil,
			"status": "confirmed", "keywords": []string{"login issue", "resolved"}, "evidence": "मेरा लॉगिन अब ठीक हो गया है।",
		}}, []map[string]any{})
	status, body = post(t, fx.handler, missing)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"code":"missing_thread_id"`)) {
		t.Fatalf("missing thread resolution was not partially rejected: status=%d body=%s", status, body)
	}
	if err := fx.db.QueryRow(`SELECT important_points_json FROM interactions WHERE source_event_id=?`, "call-hindi-resolution-missing-thread").Scan(&pointsRaw); err != nil || pointsRaw != "[]" {
		t.Fatalf("threadless resolution became trusted: err=%v points=%s", err, pointsRaw)
	}
	assertOpenThread(t, fx.db, threadID, true)

	resolved := extractionRequest("call-hindi-resolution-owned-thread", time.Date(2026, 10, 9, 10, 35, 0, 0, time.UTC),
		[]map[string]any{{"role": "user", "en_text": "My login issue is resolved now.", "indic_text": "हाँ, लॉगिन की समस्या अब हल हो गई है।"}},
		"Seller explicitly resolved the login issue.", []map[string]any{{
			"type": "resolution", "scope": "thread", "thread_id": threadID, "field": "other", "value": "resolved", "unit": nil,
			"status": "confirmed", "keywords": []string{"login issue", "resolved"}, "evidence": "हाँ, लॉगिन की समस्या अब हल हो गई है।",
		}}, []map[string]any{})
	status, body = post(t, fx.handler, resolved)
	if status != http.StatusOK {
		t.Fatalf("owned thread resolution status=%d body=%s", status, body)
	}
	assertOpenThread(t, fx.db, threadID, false)
}

func TestMultipleIssuesAbruptHangupAndTemporaryPreference(t *testing.T) {
	fx := newFixture(t)
	multiple := extractionRequest("call-multiple-issues", time.Date(2026, 10, 9, 10, 40, 0, 0, time.UTC),
		[]map[string]any{{"role": "user", "content": "My catalogue upload is failing."}, {"role": "user", "content": "My invoice is also missing."}},
		"Seller reported two independent issues.", []map[string]any{
			{"type": "issue", "scope": "thread", "thread_id": nil, "field": "catalogue_upload", "value": "upload failing", "unit": nil, "status": "reported", "keywords": []string{"catalogue upload"}, "evidence": "My catalogue upload is failing."},
			{"type": "issue", "scope": "thread", "thread_id": nil, "field": "other", "value": "invoice missing", "unit": nil, "status": "reported", "keywords": []string{"invoice missing"}, "evidence": "My invoice is also missing."},
		}, []map[string]any{})
	status, body := post(t, fx.handler, multiple)
	if status != http.StatusOK {
		t.Fatalf("multiple issues status=%d body=%s", status, body)
	}
	var pointsRaw string
	_ = fx.db.QueryRow(`SELECT important_points_json FROM interactions WHERE source_event_id=?`, "call-multiple-issues").Scan(&pointsRaw)
	var points []enrich.Point
	_ = json.Unmarshal([]byte(pointsRaw), &points)
	if len(points) != 2 || points[0].ThreadID == nil || points[1].ThreadID == nil || *points[0].ThreadID == *points[1].ThreadID {
		t.Fatalf("independent issues were not separated: %s", pointsRaw)
	}

	abrupt := extractionRequest("call-abrupt-hangup", time.Date(2026, 10, 9, 10, 45, 0, 0, time.UTC),
		[]map[string]any{{"role": "agent", "content": "Hello, are you still on the call?"}},
		"The caller did not provide a response before the call ended.", []map[string]any{}, []map[string]any{})
	if status, body := post(t, fx.handler, abrupt); status != http.StatusOK {
		t.Fatalf("abrupt hangup status=%d body=%s", status, body)
	}

	temporary := extractionRequest("call-temporary-language", time.Date(2026, 10, 9, 10, 50, 0, 0, time.UTC),
		[]map[string]any{{"role": "user", "content": "Please speak English for this call only."}},
		"Seller requested English only for this call.", []map[string]any{{
			"type": "preference", "scope": "profile", "thread_id": nil, "field": "other", "value": "English", "unit": nil,
			"status": "confirmed", "keywords": []string{"language", "preferred language"}, "evidence": "Please speak English for this call only.",
		}}, []map[string]any{{"path": "preferences.preferred_language", "value": "English", "evidence_point_index": 0}})
	status, body = post(t, fx.handler, temporary)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"reason":"temporary_call_condition"`)) {
		t.Fatalf("temporary preference was not skipped: status=%d body=%s", status, body)
	}
	var profileRaw string
	_ = fx.db.QueryRow(`SELECT profile_json FROM users WHERE glusr_id=? AND role='seller'`, "SYNTH-ENRICH-001").Scan(&profileRaw)
	if strings.Contains(profileRaw, `"preferred_language":"English"`) {
		t.Fatalf("temporary condition became a lasting profile fact: %s", profileRaw)
	}
}

func TestConcurrentIndependentProfileUpdatesPreserveBothValues(t *testing.T) {
	fx := newFixture(t)
	language := languageRequest("call-concurrent-language", "Hindi", time.Date(2026, 10, 9, 11, 0, 0, 0, time.UTC))
	callback := extractionRequest("call-concurrent-callback", time.Date(2026, 10, 9, 11, 0, 1, 0, time.UTC),
		[]map[string]any{{"role": "user", "content": "Please call me after 19:00 for future calls."}},
		"Seller set a lasting callback preference.", []map[string]any{{
			"type": "preference", "scope": "profile", "thread_id": nil, "field": "callback_after", "value": "19:00", "unit": "Asia/Kolkata",
			"status": "confirmed", "keywords": []string{"callback", "call time"}, "evidence": "Please call me after 19:00 for future calls.",
		}}, []map[string]any{{"path": "preferences.callback_after", "value": "19:00", "evidence_point_index": 0}})

	type result struct {
		status int
		body   string
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for _, request := range []map[string]any{language, callback} {
		request := request
		wait.Add(1)
		go func() {
			defer wait.Done()
			encoded, _ := json.Marshal(request)
			httpRequest := httptest.NewRequest(http.MethodPost, "/interactions/enrich", bytes.NewReader(encoded))
			httpRequest.Header.Set("Content-Type", "application/json")
			httpRequest.Header.Set("Authorization", "Bearer "+testToken)
			<-start
			recorder := httptest.NewRecorder()
			fx.handler.ServeHTTP(recorder, httpRequest)
			results <- result{status: recorder.Code, body: recorder.Body.String()}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	for item := range results {
		if item.status != http.StatusOK && item.status != http.StatusAccepted {
			t.Fatalf("concurrent update failed: status=%d body=%s", item.status, item.body)
		}
	}
	// Concurrent snapshot publications may make one response recoverable (202).
	// Identical replay must finish that rebuild without reapplying either change.
	for _, request := range []map[string]any{language, callback} {
		status, body := post(t, fx.handler, request)
		if status != http.StatusOK || !bytes.Contains(body, []byte(`"duplicate":true`)) {
			t.Fatalf("concurrent rebuild did not recover idempotently: status=%d body=%s", status, body)
		}
	}
	var profileRaw string
	if err := fx.db.QueryRow(`SELECT profile_json FROM users WHERE glusr_id=? AND role='seller'`, "SYNTH-ENRICH-001").Scan(&profileRaw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(profileRaw, `"preferred_language":"Hindi"`) || !strings.Contains(profileRaw, `"callback_after":"19:00"`) ||
		!strings.Contains(profileRaw, `"unrelated":{"keep":true}`) {
		t.Fatalf("concurrent profile merge lost data: %s", profileRaw)
	}
}

func extractionRequest(eventID string, occurred time.Time, transcript []map[string]any, summary string, points, updates []map[string]any) map[string]any {
	pointsJSON, _ := json.Marshal(points)
	innerJSON, _ := json.Marshal(map[string]any{
		"summary": summary, "important_points_json": string(pointsJSON), "profile_updates": updates,
	})
	return map[string]any{
		"glusr_id": "SYNTH-ENRICH-001", "role": "seller", "source": "sarvam", "source_event_id": eventID,
		"channel": "voice", "occurred_at": occurred.Format(time.RFC3339Nano), "session_state": "completed",
		"transcript": transcript, "enrichment_json": string(innerJSON),
	}
}

func assertOpenThread(t *testing.T, db *sql.DB, threadID string, want bool) {
	t.Helper()
	var memoryRaw string
	if err := db.QueryRow(`SELECT memory_json FROM current_context WHERE glusr_id=? AND role='seller'`, "SYNTH-ENRICH-001").Scan(&memoryRaw); err != nil {
		t.Fatal(err)
	}
	var memory struct {
		OpenThreads []struct {
			ID string `json:"id"`
		} `json:"open_threads"`
	}
	if err := json.Unmarshal([]byte(memoryRaw), &memory); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, thread := range memory.OpenThreads {
		found = found || thread.ID == threadID
	}
	if found != want {
		t.Fatalf("thread %q open=%v want=%v memory=%s", threadID, found, want, memoryRaw)
	}
}

func languageRequest(eventID, language string, occurred time.Time) map[string]any {
	evidence := "Please use " + language + " for future calls."
	points, _ := json.Marshal([]map[string]any{{"type": "preference", "scope": "profile", "thread_id": nil,
		"field": "other", "value": language, "unit": nil, "status": "confirmed",
		"keywords": []string{"language", "preferred language"}, "evidence": evidence}})
	inner, _ := json.Marshal(map[string]any{"summary": "Seller set a lasting language preference.",
		"important_points_json": string(points), "profile_updates": []map[string]any{{
			"path": "preferences.preferred_language", "value": language, "evidence_point_index": 0}}})
	return map[string]any{"glusr_id": "SYNTH-ENRICH-001", "role": "seller", "source": "sarvam",
		"source_event_id": eventID, "channel": "voice", "occurred_at": occurred.Format(time.RFC3339Nano),
		"session_state": "completed", "transcript": "USER: " + evidence, "enrichment_json": string(inner)}
}

func cloneMap(value map[string]any) map[string]any {
	encoded, _ := json.Marshal(value)
	var result map[string]any
	_ = json.Unmarshal(encoded, &result)
	return result
}
