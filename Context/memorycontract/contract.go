package memorycontract

import (
	"encoding/json"
	"strings"
)

const Version = "memory_contract_v1"

var Types = set("fact", "preference", "issue", "commitment", "correction", "resolution", "restriction")
var Scopes = set("profile", "thread")
var Fields = set("quantity", "quoted_price", "minimum_order", "callback_after", "catalogue_upload", "other")
var Statuses = set("reported", "confirmed", "disputed", "superseded")

type Point struct {
	Type     string          `json:"type"`
	Scope    string          `json:"scope"`
	ThreadID *string         `json:"thread_id"`
	Field    string          `json:"field"`
	Value    json.RawMessage `json:"value"`
	Unit     *string         `json:"unit"`
	Status   string          `json:"status"`
	Keywords []string        `json:"keywords"`
	Evidence string          `json:"evidence"`
}

func ValidType(value string) bool   { return Types[value] }
func ValidScope(value string) bool  { return Scopes[value] }
func ValidField(value string) bool  { return Fields[value] }
func ValidStatus(value string) bool { return Statuses[value] }

// EvidenceComparable performs only conservative whitespace normalization.
// It deliberately does not translate, paraphrase, transliterate, or fuzzy-match.
func EvidenceComparable(value string) string { return strings.Join(strings.Fields(value), " ") }

func set(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

const ExtractionPrompt = `Return ONLY one valid JSON object as plain text. Do not use Markdown, code fences, commentary, or a second JSON value.
The object must contain exactly: summary, important_points_json, profile_updates.

summary: 2-5 short sentences, maximum 1000 characters, describing only this conversation. Distinguish requests, estimates, commitments, corrections, and completed actions. Do not invent facts or copy loaded memory as newly stated.

important_points_json: a JSON-encoded STRING containing one JSON array with at most 30 items. Return the string "[]" when there are no supported points. Every item must contain exactly: type, scope, thread_id, field, value, unit, status, keywords, evidence.

Allowed type: fact, preference, issue, commitment, correction, resolution, restriction.
Allowed scope: profile, thread.
Allowed field: quantity, quoted_price, minimum_order, callback_after, catalogue_upload, other.
Allowed status: reported, confirmed, disputed, superseded.
value must be a string, number, boolean, or null. unit must be a string or null. keywords must contain 1-8 short evidence-supported phrases.

evidence must be copied exactly from a USER/CALLER transcript text variant delivered to the end hook. When transcript turns contain en_text and indic_text, copy one of those values exactly; never translate, paraphrase, repair punctuation, or quote assistant speech.

For an existing issue, correction, or resolution, copy the exact known thread ID shown in the loaded context as [thread_id: ...]. A resolution or thread correction without a known thread ID must use null and will be retained as rejected structured output until corrected. Never invent a thread ID. A new issue must use thread_id null; the backend assigns its ID. Keep multiple issues as separate points.

Use profile scope only for explicit lasting preferences. Use thread scope for a temporary condition or a particular enquiry/issue. Do not turn "this call", "today only", "for now", guesses, assistant suggestions, or personality inferences into lasting profile facts.

Use field=other with keywords "language" and "preferred language" for a lasting language preference; field=callback_after with HH:MM for a lasting callback preference; field=other with keyword "timezone" for timezone; field=other with keyword "communication style" for an explicit lasting style.

profile_updates: an array. Each item must contain exactly path, value, evidence_point_index. The index is the original zero-based index in important_points_json. Allowed paths: preferences.preferred_language, preferences.callback_after, preferences.timezone, preferences.communication_style. Propose an update only for an explicit lasting preference supported by that exact point.

Do not include SQL, internal user IDs, backend timestamps, processing states, or Markdown.`
