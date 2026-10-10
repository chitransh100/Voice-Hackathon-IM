package enrich

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"vani-context/enrichmentapi/shared"
	"vani-context/memorycontract"
)

var callbackPattern = regexp.MustCompile(`^(?:[01]\d|2[0-3]):[0-5]\d$`)

var allowedChannels = stringSet("voice", "whatsapp", "web_chat", "app", "enquiry", "other")

const sarvamStartDatetimeLayout = "2006-01-02 15:04:05"
const sarvamStartDatetimeTLayout = "2006-01-02T15:04:05"

func ValidateEnvelope(input Request, sourceTimezone *time.Location) (Prepared, string, error) {
	originalSource, originalChannel := input.Source, input.Channel
	input.GLUSRID = strings.TrimSpace(input.GLUSRID)
	input.Role = strings.ToLower(strings.TrimSpace(input.Role))
	input.Source = normalizeSource(input.Source)
	input.SourceEventID = strings.TrimSpace(input.SourceEventID)
	input.Channel = normalizeChannel(input.Channel)
	input.SessionState = strings.ToLower(strings.TrimSpace(input.SessionState))
	if !shared.IdentifierPattern.MatchString(input.GLUSRID) {
		return Prepared{}, "invalid_identity", errors.New("glusr_id has an invalid format")
	}
	if err := shared.ParseRole(input.Role); err != nil {
		return Prepared{}, "invalid_role", err
	}
	if input.Source == "" || utf8.RuneCountInString(input.Source) > 128 || containsControl(originalSource) || !shared.SourceEventIDPattern.MatchString(input.SourceEventID) {
		return Prepared{}, "invalid_source_identity", errors.New("source must be printable text and source_event_id must use the documented identifier format")
	}
	if !allowedChannels[input.Channel] {
		return Prepared{}, "invalid_channel", errors.New("channel is not supported")
	}
	if input.SessionState != "ongoing" && input.SessionState != "completed" {
		return Prepared{}, "invalid_session_state", errors.New("session_state must be ongoing or completed")
	}
	occurred, eventTime, code, err := normalizeEventTime(input.OccurredAt, input.StartDatetime, sourceTimezone)
	if err != nil {
		return Prepared{}, code, err
	}
	if len(input.Transcript) == 0 || utf8.RuneCountInString(input.Transcript) > 200000 {
		return Prepared{}, "invalid_transcript", errors.New("transcript must contain 1-200000 characters")
	}
	var thread *string
	if input.ThreadID != nil {
		value := strings.TrimSpace(*input.ThreadID)
		if !shared.IdentifierPattern.MatchString(value) {
			return Prepared{}, "invalid_thread", errors.New("thread_id has an invalid format")
		}
		thread = &value
	}
	normalized := input
	normalized.OccurredAt = shared.FormatUTC(occurred)
	normalized.StartDatetime = ""
	normalized.ThreadID = thread
	canonical, _ := json.Marshal(normalized)
	hash := sha256.Sum256(canonical)
	return Prepared{Request: normalized, CanonicalHash: hex.EncodeToString(hash[:]),
		NormalizedTime: normalized.OccurredAt, NormalizedThread: thread, EventTime: eventTime,
		OriginalSource: originalSource, OriginalChannel: originalChannel}, "", nil
}

func ValidateExtraction(prepared Prepared) (Prepared, string, error) {
	input := prepared.Request
	enrichment, err := decodeEnrichment(input.EnrichmentJSON)
	if err != nil {
		return prepared, "invalid_enrichment_json", err
	}
	if strings.TrimSpace(enrichment.Summary) == "" || utf8.RuneCountInString(enrichment.Summary) > 1000 {
		return prepared, "invalid_summary", errors.New("summary must contain 1-1000 characters")
	}
	points, trustedPoints, trustedIndexes, pointsJSON, evidenceOK, warnings, err := decodePoints(enrichment.ImportantPointsJSON, input.TranscriptTurns, prepared.NormalizedThread)
	if err != nil {
		return prepared, "invalid_important_points", err
	}
	updates := make([]ValidatedUpdate, 0, len(enrichment.ProfileUpdates))
	if len(enrichment.ProfileUpdates) > 20 {
		return prepared, "invalid_profile_updates", errors.New("profile_updates cannot exceed 20 entries")
	}
	seenPaths := map[string]bool{}
	for index, update := range enrichment.ProfileUpdates {
		validated := validateUpdate(index, update, points, evidenceOK)
		if validated.PrecheckReason == "" && seenPaths[validated.Path] {
			validated.PrecheckReason = "duplicate_path_in_request"
		}
		if validated.Path != "" {
			seenPaths[validated.Path] = true
		}
		updates = append(updates, validated)
	}
	prepared.Enrichment = enrichment
	prepared.Points = trustedPoints
	prepared.TrustedPointIndexes = trustedIndexes
	prepared.PointsJSON = pointsJSON
	prepared.SubmittedPoints = points
	prepared.PointEvidenceOK = evidenceOK
	prepared.ValidationWarnings = warnings
	prepared.Updates = updates
	return prepared, "", nil
}

func normalizeSource(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), "_"))
}

func normalizeChannel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	value = strings.NewReplacer("-", "_", " ", "_").Replace(value)
	switch value {
	case "voice", "call", "phone", "phone_call", "telephone", "telephony":
		return "voice"
	case "whatsapp", "whats_app", "wa":
		return "whatsapp"
	case "web_chat", "webchat", "web", "chat":
		return "web_chat"
	case "app", "mobile_app":
		return "app"
	case "enquiry", "inquiry", "lead":
		return "enquiry"
	case "other":
		return "other"
	default:
		return "other"
	}
}

func containsControl(value string) bool {
	for _, current := range value {
		if current < 0x20 || current == 0x7f {
			return true
		}
	}
	return false
}

func normalizeEventTime(occurredAt, startDatetime string, sourceTimezone *time.Location) (time.Time, EventTimeMetadata, string, error) {
	occurredAt = strings.TrimSpace(occurredAt)
	startDatetime = strings.TrimSpace(startDatetime)
	if (occurredAt == "") == (startDatetime == "") {
		return time.Time{}, EventTimeMetadata{}, "invalid_event_time", errors.New("provide exactly one of occurred_at or start_datetime")
	}
	if occurredAt != "" {
		occurred, err := time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			return time.Time{}, EventTimeMetadata{}, "invalid_occurred_at", errors.New("occurred_at must be a timezone-aware RFC3339 timestamp")
		}
		normalized := shared.FormatUTC(occurred)
		return occurred, EventTimeMetadata{InputField: "occurred_at", RawValue: occurredAt, NormalizedUTC: normalized}, "", nil
	}
	if sourceTimezone == nil {
		return time.Time{}, EventTimeMetadata{}, "source_timezone_not_configured", errors.New("start_datetime cannot be interpreted without the configured workflow timezone")
	}
	layout := sarvamStartDatetimeLayout
	if strings.Contains(startDatetime, "T") {
		layout = sarvamStartDatetimeTLayout
	}
	started, err := time.ParseInLocation(layout, startDatetime, sourceTimezone)
	if err != nil || started.Format(layout) != startDatetime {
		return time.Time{}, EventTimeMetadata{}, "invalid_start_datetime", errors.New("start_datetime must use Sarvam's YYYY-MM-DD HH:MM:SS or YYYY-MM-DDTHH:MM:SS format")
	}
	normalized := shared.FormatUTC(started)
	return started, EventTimeMetadata{InputField: "start_datetime", RawValue: startDatetime,
		SourceTimezone: sourceTimezone.String(), NormalizedUTC: normalized}, "", nil
}

func decodeEnrichment(raw string) (Enrichment, error) {
	if strings.TrimSpace(raw) == "" || len(raw) > 512000 {
		return Enrichment{}, errors.New("enrichment_json must be a nonempty JSON string no larger than 512000 bytes")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value Enrichment
	if err := decoder.Decode(&value); err != nil {
		return Enrichment{}, fmt.Errorf("parse enrichment_json: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return Enrichment{}, err
	}
	return value, nil
}

func decodePoints(raw string, turns []TranscriptTurn, outerThread *string) ([]Point, []Point, []int, []byte, []bool, []ValidationWarning, error) {
	if strings.TrimSpace(raw) == "" || len(raw) > 256000 {
		return nil, nil, nil, nil, nil, nil, errors.New("important_points_json must be a nonempty JSON array string")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var points []Point
	if err := decoder.Decode(&points); err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("parse important_points_json: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}
	if points == nil || len(points) > 30 {
		return nil, nil, nil, nil, nil, nil, errors.New("important_points_json must contain 0-30 points")
	}
	trusted := make([]Point, 0, len(points))
	trustedIndexes := make([]int, 0, len(points))
	evidenceOK := make([]bool, len(points))
	warnings := []ValidationWarning{}
	for index := range points {
		if err := validatePointStructure(points[index], outerThread); err != nil {
			return nil, nil, nil, nil, nil, nil, fmt.Errorf("point %d: %w", index, err)
		}
		if code, err := validatePointEvidence(points[index], turns); err != nil {
			warnings = append(warnings, ValidationWarning{Code: code, PointIndex: index, Message: err.Error()})
			continue
		}
		evidenceOK[index] = true
		trusted = append(trusted, points[index])
		trustedIndexes = append(trustedIndexes, index)
	}
	encoded, _ := json.Marshal(trusted)
	return points, trusted, trustedIndexes, encoded, evidenceOK, warnings, nil
}

func validatePointStructure(point Point, outerThread *string) error {
	if !memorycontract.ValidType(point.Type) || !memorycontract.ValidScope(point.Scope) || !memorycontract.ValidField(point.Field) || !memorycontract.ValidStatus(point.Status) {
		return errors.New("type, scope, field, or status is outside the shared enum")
	}
	if len(point.Value) == 0 || !isScalar(point.Value) {
		return errors.New("value must be a JSON string, number, boolean, or null")
	}
	if point.Unit != nil && utf8.RuneCountInString(*point.Unit) > 64 {
		return errors.New("unit exceeds 64 characters")
	}
	if len(point.Keywords) < 1 || len(point.Keywords) > 8 {
		return errors.New("keywords must contain 1-8 values")
	}
	for _, keyword := range point.Keywords {
		if strings.TrimSpace(keyword) == "" || utf8.RuneCountInString(keyword) > 64 {
			return errors.New("each keyword must contain 1-64 characters")
		}
	}
	if point.Evidence == "" || utf8.RuneCountInString(point.Evidence) > 1000 {
		return errors.New("evidence must contain 1-1000 characters")
	}
	if point.Scope == "profile" && point.ThreadID != nil {
		return errors.New("profile-scoped points require a null thread_id")
	}
	if point.ThreadID != nil {
		if !shared.IdentifierPattern.MatchString(*point.ThreadID) {
			return errors.New("point thread_id has an invalid format")
		}
	}
	return nil
}

func validatePointEvidence(point Point, turns []TranscriptTurn) (string, error) {
	target := memorycontract.EvidenceComparable(point.Evidence)
	foundNonUser := false
	for _, turn := range turns {
		for _, variant := range turn.Variants {
			if memorycontract.EvidenceComparable(variant.Text) != target {
				continue
			}
			if turn.Role == "USER" {
				return "", nil
			}
			foundNonUser = true
		}
	}
	if foundNonUser {
		return "non_user_evidence", errors.New("evidence appears only in a non-user transcript turn; point excluded from trusted memory")
	}
	return "unsupported_evidence", errors.New("evidence is not an exact user transcript variant; point excluded from trusted memory")
}

func validateUpdate(index int, update ProfileUpdate, points []Point, evidenceOK []bool) ValidatedUpdate {
	result := ValidatedUpdate{Index: index, Path: normalizePath(update.Path), Value: append(json.RawMessage(nil), update.Value...), EvidencePointIndex: update.EvidencePointIndex}
	if !supportedPath(result.Path) {
		result.PrecheckReason = "unsupported_path"
		return result
	}
	if update.EvidencePointIndex < 0 || update.EvidencePointIndex >= len(points) {
		result.PrecheckReason = "missing_evidence_point"
		return result
	}
	if !evidenceOK[update.EvidencePointIndex] {
		result.PrecheckReason = "referenced_point_has_unsupported_evidence"
		return result
	}
	point := points[update.EvidencePointIndex]
	result.Evidence = point.Evidence
	if point.Type != "preference" && point.Type != "correction" {
		result.PrecheckReason = "evidence_point_is_not_a_preference_or_correction"
		return result
	}
	if point.Scope != "profile" || point.Status != "confirmed" {
		result.PrecheckReason = "profile_change_requires_confirmed_profile_scope"
		return result
	}
	if !jsonScalarEqual(update.Value, point.Value) {
		result.PrecheckReason = "value_not_supported_by_referenced_point"
		return result
	}
	if !pointMapsToPath(point, result.Path) {
		result.PrecheckReason = "point_field_or_keywords_do_not_map_to_path"
		return result
	}
	if temporaryEvidence(point.Evidence) {
		result.PrecheckReason = "temporary_call_condition"
		return result
	}
	if err := validateProfileValue(result.Path, update.Value); err != nil {
		result.PrecheckReason = err.Error()
	}
	return result
}

func supportedPath(path string) bool {
	switch path {
	case "preferences.preferred_language", "preferences.callback_after", "preferences.timezone", "preferences.communication_style":
		return true
	default:
		return false
	}
}

func normalizePath(path string) string {
	value := strings.TrimSpace(path)
	if strings.HasPrefix(value, "/") {
		value = strings.TrimPrefix(value, "/")
		value = strings.ReplaceAll(value, "/", ".")
	}
	return value
}

func pointMapsToPath(point Point, path string) bool {
	keywords := map[string]bool{}
	for _, keyword := range point.Keywords {
		keywords[strings.ToLower(strings.Join(strings.Fields(keyword), " "))] = true
	}
	switch path {
	case "preferences.callback_after":
		return point.Field == "callback_after"
	case "preferences.preferred_language":
		return point.Field == "other" && (keywords["language"] || keywords["preferred language"])
	case "preferences.timezone":
		return point.Field == "other" && (keywords["timezone"] || keywords["time zone"])
	case "preferences.communication_style":
		return point.Field == "other" && (keywords["communication style"] || keywords["call style"])
	}
	return false
}

func validateProfileValue(path string, raw json.RawMessage) error {
	var value string
	if json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) != value || value == "" || utf8.RuneCountInString(value) > 64 {
		return errors.New("profile_value_must_be_a_trimmed_string_of_1_to_64_characters")
	}
	switch path {
	case "preferences.callback_after":
		if !callbackPattern.MatchString(value) {
			return errors.New("callback_after_must_use_HH_MM_24_hour_format")
		}
	case "preferences.timezone":
		if _, err := time.LoadLocation(value); err != nil {
			return errors.New("timezone_must_be_a_valid_IANA_name")
		}
	case "preferences.communication_style":
		allowed := stringSet("concise", "detailed", "formal", "informal")
		if !allowed[strings.ToLower(value)] {
			return errors.New("communication_style_is_not_allowlisted")
		}
	}
	return nil
}

func temporaryEvidence(value string) bool {
	normalized := strings.ToLower(value)
	for _, marker := range []string{"this call", "for today", "today only", "for now", "just this time", "temporarily"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func isScalar(raw json.RawMessage) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil || requireEOF(decoder) != nil {
		return false
	}
	switch value.(type) {
	case nil, string, bool, json.Number:
		return true
	default:
		return false
	}
}

func jsonScalarEqual(left, right json.RawMessage) bool {
	var a, b any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	leftDecoder.UseNumber()
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&a) != nil || rightDecoder.Decode(&b) != nil {
		return false
	}
	return fmt.Sprint(a) == fmt.Sprint(b) && fmt.Sprintf("%T", a) == fmt.Sprintf("%T", b)
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return errors.New("JSON must contain exactly one value")
}

func stringSet(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func interactionID(prepared Prepared) string {
	hash := sha256.Sum256([]byte(prepared.Request.Source + "\x00" + prepared.Request.SourceEventID + "\x00" + prepared.Request.GLUSRID + "\x00" + prepared.Request.Role))
	return "enrich-" + hex.EncodeToString(hash[:12])
}
