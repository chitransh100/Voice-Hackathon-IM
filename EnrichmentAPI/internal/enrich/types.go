package enrich

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"vani-context/memorycontract"
)

type Request struct {
	GLUSRID         string           `json:"glusr_id"`
	Role            string           `json:"role"`
	Source          string           `json:"source"`
	SourceEventID   string           `json:"source_event_id"`
	Channel         string           `json:"channel"`
	OccurredAt      string           `json:"occurred_at,omitempty"`
	StartDatetime   string           `json:"start_datetime,omitempty"`
	SessionState    string           `json:"session_state"`
	ThreadID        *string          `json:"thread_id,omitempty"`
	Transcript      string           `json:"transcript"`
	EnrichmentJSON  string           `json:"enrichment_json"`
	TranscriptRaw   json.RawMessage  `json:"-"`
	EnrichmentRaw   json.RawMessage  `json:"-"`
	TranscriptTurns []TranscriptTurn `json:"-"`
}

// UnmarshalJSON accepts both the API's canonical string fields and Sarvam's
// structured runtime values, then normalizes them into the existing contract.
func (r *Request) UnmarshalJSON(data []byte) error {
	type wireRequest struct {
		GLUSRID        string          `json:"glusr_id"`
		Role           string          `json:"role"`
		Source         string          `json:"source"`
		SourceEventID  string          `json:"source_event_id"`
		Channel        string          `json:"channel"`
		OccurredAt     string          `json:"occurred_at,omitempty"`
		StartDatetime  string          `json:"start_datetime,omitempty"`
		SessionState   string          `json:"session_state"`
		ThreadID       *string         `json:"thread_id,omitempty"`
		Transcript     json.RawMessage `json:"transcript"`
		EnrichmentJSON json.RawMessage `json:"enrichment_json"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire wireRequest
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if err := requireEOF(decoder); err != nil {
		return err
	}
	transcript, turns, err := normalizeTranscript(wire.Transcript)
	if err != nil {
		return fmt.Errorf("transcript: %w", err)
	}
	enrichmentJSON, err := normalizeEnrichmentJSON(wire.EnrichmentJSON)
	if err != nil {
		return fmt.Errorf("enrichment_json: %w", err)
	}
	*r = Request{
		GLUSRID: wire.GLUSRID, Role: wire.Role, Source: wire.Source, SourceEventID: wire.SourceEventID,
		Channel: wire.Channel, OccurredAt: wire.OccurredAt, StartDatetime: wire.StartDatetime,
		SessionState: wire.SessionState, ThreadID: wire.ThreadID, Transcript: transcript,
		EnrichmentJSON: enrichmentJSON, TranscriptRaw: append(json.RawMessage(nil), wire.Transcript...),
		EnrichmentRaw: append(json.RawMessage(nil), wire.EnrichmentJSON...), TranscriptTurns: turns,
	}
	return nil
}

type transcriptTurn struct {
	Role      string  `json:"role"`
	EnText    *string `json:"en_text"`
	IndicText *string `json:"indic_text"`
	Content   *string `json:"content"`
}

type TranscriptVariant struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type TranscriptTurn struct {
	Role     string              `json:"role"`
	Variants []TranscriptVariant `json:"variants"`
}

func normalizeTranscript(raw json.RawMessage) (string, []TranscriptTurn, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil, nil
	}
	if trimmed[0] == '"' {
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return "", nil, err
		}
		return value, parseLabelledTranscript(value), nil
	}
	if trimmed[0] != '[' {
		return "", nil, errors.New("must be a string or an array of Sarvam transcript turns")
	}
	var turns []transcriptTurn
	if err := json.Unmarshal(trimmed, &turns); err != nil {
		return "", nil, err
	}
	lines := make([]string, 0, len(turns)*2)
	normalizedTurns := make([]TranscriptTurn, 0, len(turns))
	for index, turn := range turns {
		role := strings.ToUpper(strings.TrimSpace(turn.Role))
		switch role {
		case "AGENT", "ASSISTANT", "BOT", "SUPPORT", "VANI":
			role = "AGENT"
		case "USER", "CALLER":
			role = "USER"
		default:
			return "", nil, fmt.Errorf("turn %d has unsupported role", index)
		}
		variants := []TranscriptVariant{}
		for variantIndex, candidate := range []*string{turn.EnText, turn.IndicText, turn.Content} {
			if candidate == nil || strings.TrimSpace(*candidate) == "" {
				continue
			}
			value := strings.TrimSpace(*candidate)
			kind := []string{"en_text", "indic_text", "content"}[variantIndex]
			duplicate := false
			for _, prior := range variants {
				if prior.Text == value {
					duplicate = true
					break
				}
			}
			if !duplicate {
				variants = append(variants, TranscriptVariant{Kind: kind, Text: value})
			}
		}
		if len(variants) == 0 {
			return "", nil, fmt.Errorf("turn %d has no transcript text", index)
		}
		for _, variant := range variants {
			lines = append(lines, role+": "+variant.Text)
		}
		normalizedTurns = append(normalizedTurns, TranscriptTurn{Role: role, Variants: variants})
	}
	return strings.Join(lines, "\n"), normalizedTurns, nil
}

func parseLabelledTranscript(value string) []TranscriptTurn {
	result := []TranscriptTurn{}
	for _, line := range strings.Split(value, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
			continue
		}
		role := strings.ToUpper(strings.TrimSpace(parts[0]))
		switch role {
		case "AGENT", "ASSISTANT", "BOT", "SUPPORT", "VANI":
			role = "AGENT"
		case "USER", "CALLER":
			role = "USER"
		default:
			continue
		}
		result = append(result, TranscriptTurn{Role: role, Variants: []TranscriptVariant{{Kind: "labelled_text", Text: strings.TrimSpace(parts[1])}}})
	}
	return result
}

func normalizeEnrichmentJSON(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	if trimmed[0] == '"' {
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return "", err
		}
		return value, nil
	}
	if trimmed[0] != '{' {
		return "", errors.New("must be a JSON-encoded string or object")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return "", err
	}
	return compact.String(), nil
}

type Enrichment struct {
	Summary             string          `json:"summary"`
	ImportantPointsJSON string          `json:"important_points_json"`
	ProfileUpdates      []ProfileUpdate `json:"profile_updates"`
}

type ProfileUpdate struct {
	Path               string          `json:"path"`
	Value              json.RawMessage `json:"value"`
	EvidencePointIndex int             `json:"evidence_point_index"`
}

type Point = memorycontract.Point

type Prepared struct {
	Request             Request
	OriginalSource      string
	OriginalChannel     string
	Enrichment          Enrichment
	Points              []Point
	PointsJSON          []byte
	SubmittedPoints     []Point
	PointEvidenceOK     []bool
	ValidationWarnings  []ValidationWarning
	TrustedPointIndexes []int
	CanonicalHash       string
	NormalizedTime      string
	NormalizedThread    *string
	Updates             []ValidatedUpdate
	EventTime           EventTimeMetadata
	InteractionID       string
	AcceptedRevision    int
	DuplicateDelivery   bool
}

type ValidationWarning struct {
	Code       string `json:"code"`
	PointIndex int    `json:"point_index"`
	Message    string `json:"message"`
}

type ValidationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type EventTimeMetadata struct {
	InputField     string `json:"input_field"`
	RawValue       string `json:"raw_value"`
	SourceTimezone string `json:"source_timezone,omitempty"`
	NormalizedUTC  string `json:"normalized_utc"`
}

type InputAdapterMetadata struct {
	OriginalSource   string `json:"original_source"`
	CanonicalSource  string `json:"canonical_source"`
	OriginalChannel  string `json:"original_channel"`
	CanonicalChannel string `json:"canonical_channel"`
}

type ValidatedUpdate struct {
	Index              int
	Path               string
	Value              json.RawMessage
	EvidencePointIndex int
	Evidence           string
	PrecheckReason     string
}

type ChangeResult struct {
	Path               string          `json:"path"`
	Value              json.RawMessage `json:"value"`
	EvidencePointIndex int             `json:"evidence_point_index"`
	Reason             string          `json:"reason,omitempty"`
}

type Response struct {
	InteractionID      string              `json:"interaction_id"`
	AcceptedRevision   int                 `json:"accepted_revision"`
	ProcessingStatus   string              `json:"processing_status"`
	Duplicate          bool                `json:"duplicate"`
	ContextVersion     *int64              `json:"context_version,omitempty"`
	AppliedChanges     []ChangeResult      `json:"applied_changes"`
	SkippedChanges     []ChangeResult      `json:"skipped_changes"`
	ConflictingChanges []ChangeResult      `json:"conflicting_changes"`
	ValidationWarnings []ValidationWarning `json:"validation_warnings"`
	ExtractionStatus   string              `json:"extraction_status"`
	ValidationErrors   []ValidationError   `json:"validation_errors"`
}

type StoredChange struct {
	Path               string          `json:"path"`
	Value              json.RawMessage `json:"value"`
	EvidencePointIndex int             `json:"evidence_point_index"`
	Evidence           string          `json:"evidence"`
	Status             string          `json:"status"`
	Reason             string          `json:"reason,omitempty"`
	EffectiveAt        string          `json:"effective_at"`
	Revision           int             `json:"revision"`
	PreviousValue      json.RawMessage `json:"previous_value,omitempty"`
	PreviousExisted    bool            `json:"previous_existed"`
}
