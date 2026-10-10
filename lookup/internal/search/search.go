package search

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"vani-context/memorycontract"
	"vani-lookup/internal/store"
)

const (
	SearchMethod      = "shared_keyword_contract_v1"
	maxRelatedUpdates = 5
	maxResponseBytes  = 8 << 10
	maxEvidenceRunes  = 240
	maxSummaryRunes   = 400
)

type Query struct {
	GLUSRID  string
	Role     string
	Topic    string
	ThreadID *string
	From     string
	To       string
	Limit    int
}

type Point struct {
	Type     string   `json:"type"`
	Scope    string   `json:"scope"`
	ThreadID *string  `json:"thread_id"`
	Field    string   `json:"field"`
	Value    any      `json:"value"`
	Unit     *string  `json:"unit"`
	Status   string   `json:"status"`
	Keywords []string `json:"keywords,omitempty"`
	Evidence string   `json:"evidence,omitempty"`
}

type Match struct {
	InteractionID      string   `json:"interaction_id"`
	OccurredAt         string   `json:"occurred_at"`
	Channel            string   `json:"channel"`
	ThreadID           *string  `json:"thread_id"`
	Source             string   `json:"source"`
	SourceEventID      string   `json:"source_event_id"`
	Summary            string   `json:"summary,omitempty"`
	ImportantPoints    []Point  `json:"important_points"`
	SupportingEvidence []string `json:"supporting_evidence,omitempty"`
	EvidenceAvailable  bool     `json:"evidence_available"`
	ExtractionPending  bool     `json:"extraction_pending"`
	Score              int      `json:"score"`
}

type RelatedUpdate struct {
	InteractionID string  `json:"interaction_id"`
	OccurredAt    string  `json:"occurred_at"`
	Channel       string  `json:"channel"`
	ThreadID      *string `json:"thread_id"`
	Source        string  `json:"source"`
	SourceEventID string  `json:"source_event_id"`
	Point         Point   `json:"point"`
}

type Response struct {
	Status                string          `json:"status"`
	Matches               []Match         `json:"matches"`
	RelatedUpdates        []RelatedUpdate `json:"related_updates"`
	SearchMethod          string          `json:"search_method"`
	SearchScopeLimited    bool            `json:"search_scope_limited"`
	RelatedUpdatesLimited bool            `json:"related_updates_limited"`
	ResponseTruncated     bool            `json:"response_truncated"`
	SearchedFrom          *string         `json:"searched_from"`
	SearchedTo            *string         `json:"searched_to"`
	HistoryText           string          `json:"history_text"`
}

type Engine struct {
	store          *store.Store
	candidateLimit int
}

type ranked struct {
	match     Match
	allPoints []Point
	rowThread *string
}

func New(repository *store.Store, candidateLimit int) *Engine {
	return &Engine{store: repository, candidateLimit: candidateLimit}
}

func (e *Engine) Search(ctx context.Context, query Query) (Response, error) {
	terms := Terms(query.Topic)
	if len(terms) == 0 && query.ThreadID == nil {
		return Response{}, fmt.Errorf("topic has no searchable terms")
	}
	if query.ThreadID != nil {
		owned, err := e.store.ThreadOwned(ctx, query.GLUSRID, query.Role, *query.ThreadID)
		if err != nil {
			return Response{}, err
		}
		if !owned {
			return Response{}, ErrThreadNotOwned
		}
	}
	candidates, limited, err := e.store.Candidates(ctx, query.GLUSRID, query.Role, query.From, query.To, e.candidateLimit)
	if err != nil {
		return Response{}, err
	}
	var rankedMatches []ranked
	for _, candidate := range candidates {
		item, ok, err := rankCandidate(candidate, terms, query.ThreadID)
		if err != nil {
			return Response{}, err
		}
		if ok {
			rankedMatches = append(rankedMatches, item)
		}
	}
	sort.Slice(rankedMatches, func(i, j int) bool {
		if rankedMatches[i].match.Score != rankedMatches[j].match.Score {
			return rankedMatches[i].match.Score > rankedMatches[j].match.Score
		}
		if rankedMatches[i].match.OccurredAt != rankedMatches[j].match.OccurredAt {
			return rankedMatches[i].match.OccurredAt > rankedMatches[j].match.OccurredAt
		}
		return rankedMatches[i].match.InteractionID < rankedMatches[j].match.InteractionID
	})
	if len(rankedMatches) > query.Limit {
		rankedMatches = rankedMatches[:query.Limit]
	}
	response := Response{
		Status: "not_found", Matches: []Match{}, RelatedUpdates: []RelatedUpdate{}, SearchMethod: SearchMethod,
		SearchScopeLimited: limited, SearchedFrom: nullableString(query.From), SearchedTo: nullableString(query.To),
		HistoryText: "No matching historical evidence was found.",
	}
	if len(rankedMatches) == 0 {
		return response, nil
	}
	response.Status = "found"
	for _, item := range rankedMatches {
		response.Matches = append(response.Matches, item.match)
	}
	updates, updatesLimited, err := e.relatedUpdates(ctx, query, rankedMatches)
	if err != nil {
		return Response{}, err
	}
	response.RelatedUpdates = updates
	response.RelatedUpdatesLimited = updatesLimited
	response.HistoryText = renderHistory(response.Matches, response.RelatedUpdates)
	boundResponse(&response)
	return response, nil
}

var ErrThreadNotOwned = fmt.Errorf("thread is not owned by requested identity")

func rankCandidate(candidate store.Candidate, terms []string, threadFilter *string) (ranked, bool, error) {
	trustedCurrent := candidate.ProcessingStatus == "processed" && candidate.ProcessedRevision >= candidate.SourceRevision
	points := []Point{}
	var err error
	if trustedCurrent {
		points, err = parsePoints(candidate.ImportantPointsJSON)
	}
	if err != nil {
		return ranked{}, false, fmt.Errorf("interaction %s has invalid important points: %w", candidate.InteractionID, err)
	}
	rowThreadMatches := threadFilter == nil || (candidate.ThreadID != nil && *candidate.ThreadID == *threadFilter)
	var relevant []Point
	for _, point := range points {
		if threadFilter == nil || (point.ThreadID != nil && *point.ThreadID == *threadFilter) {
			relevant = append(relevant, point)
		}
	}
	if threadFilter != nil && !rowThreadMatches && len(relevant) == 0 {
		return ranked{}, false, nil
	}
	summary := ""
	if trustedCurrent && candidate.Summary != nil && rowThreadMatches {
		summary = *candidate.Summary
	}
	payloadEvidence := authorisedPayloadText(candidate.SourcePayloadJSON)
	if !rowThreadMatches && threadFilter != nil {
		payloadEvidence = ""
	}
	score := 0
	matchedPointIndexes := map[int]bool{}
	matchedEvidence := []string{}
	for _, term := range terms {
		best := 0
		for index, point := range relevant {
			if exactStructuredMatch(point, term) {
				if best < 3 {
					best = 3
				}
				matchedPointIndexes[index] = true
			} else if containsTerm(pointSearchText(point), term) {
				if best < 2 {
					best = 2
				}
				matchedPointIndexes[index] = true
			}
		}
		if best == 0 && (containsTerm(Normalize(summary), term) || containsTerm(Normalize(payloadEvidence), term)) {
			best = 1
		}
		score += best
	}
	if threadFilter != nil && len(terms) == 0 {
		score = 1
		for index := range relevant {
			matchedPointIndexes[index] = true
		}
	}
	if score == 0 {
		return ranked{}, false, nil
	}
	matchedPoints := make([]Point, 0, len(matchedPointIndexes))
	for index, point := range relevant {
		if matchedPointIndexes[index] && len(matchedPoints) < 3 {
			point.Evidence = truncate(point.Evidence, maxEvidenceRunes)
			matchedPoints = append(matchedPoints, point)
			if point.Evidence != "" {
				matchedEvidence = appendUnique(matchedEvidence, point.Evidence)
			}
		}
	}
	if len(matchedEvidence) == 0 && payloadEvidence != "" {
		matchedEvidence = append(matchedEvidence, truncate(payloadEvidence, maxEvidenceRunes))
	}
	threadID := candidate.ThreadID
	if threadFilter != nil {
		threadID = threadFilter
	}
	match := Match{
		InteractionID: candidate.InteractionID, OccurredAt: candidate.OccurredAt, Channel: candidate.Channel,
		ThreadID: threadID, Source: candidate.Source, SourceEventID: candidate.SourceEventID,
		Summary: truncate(summary, maxSummaryRunes), ImportantPoints: matchedPoints,
		SupportingEvidence: matchedEvidence, EvidenceAvailable: len(matchedEvidence) > 0,
		ExtractionPending: candidate.ProcessingStatus != "processed" || candidate.ProcessedRevision < candidate.SourceRevision,
		Score:             score,
	}
	return ranked{match: match, allPoints: relevant, rowThread: candidate.ThreadID}, true, nil
}

func (e *Engine) relatedUpdates(ctx context.Context, query Query, matches []ranked) ([]RelatedUpdate, bool, error) {
	updates := []RelatedUpdate{}
	seen := map[string]bool{}
	limited := false
	for _, match := range matches {
		for _, matchedPoint := range match.match.ImportantPoints {
			if !linkable(matchedPoint) {
				continue
			}
			candidates, candidateLimited, err := e.store.LaterCandidates(ctx, query.GLUSRID, query.Role, match.match.OccurredAt, query.To, e.candidateLimit)
			if err != nil {
				return nil, false, err
			}
			limited = limited || candidateLimited
			for _, candidate := range candidates {
				if candidate.ProcessingStatus != "processed" || candidate.ProcessedRevision < candidate.SourceRevision {
					continue
				}
				points, err := parsePoints(candidate.ImportantPointsJSON)
				if err != nil {
					return nil, false, err
				}
				for _, update := range points {
					if !isExplicitUpdate(update) || !sameRelationship(matchedPoint, update) {
						continue
					}
					key := candidate.InteractionID + "|" + update.Type + "|" + update.Field + "|" + update.Evidence
					if seen[key] {
						continue
					}
					seen[key] = true
					update.Evidence = truncate(update.Evidence, maxEvidenceRunes)
					updates = append(updates, RelatedUpdate{InteractionID: candidate.InteractionID,
						OccurredAt: candidate.OccurredAt, Channel: candidate.Channel, ThreadID: update.ThreadID,
						Source: candidate.Source, SourceEventID: candidate.SourceEventID, Point: update})
					if len(updates) == maxRelatedUpdates {
						return updates, true, nil
					}
				}
			}
		}
	}
	sort.Slice(updates, func(i, j int) bool {
		if updates[i].OccurredAt != updates[j].OccurredAt {
			return updates[i].OccurredAt < updates[j].OccurredAt
		}
		return updates[i].InteractionID < updates[j].InteractionID
	})
	return updates, limited, nil
}

func parsePoints(raw string) ([]Point, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var points []Point
	if err := decoder.Decode(&points); err != nil {
		return nil, err
	}
	if points == nil {
		points = []Point{}
	}
	for index, point := range points {
		if !memorycontract.ValidType(point.Type) || !memorycontract.ValidScope(point.Scope) || !memorycontract.ValidField(point.Field) || !memorycontract.ValidStatus(point.Status) {
			return nil, fmt.Errorf("point %d violates %s", index, memorycontract.Version)
		}
	}
	return points, nil
}

func exactStructuredMatch(point Point, term string) bool {
	if Normalize(point.Field) == term {
		return true
	}
	for _, keyword := range point.Keywords {
		if Normalize(keyword) == term {
			return true
		}
	}
	return false
}

func pointSearchText(point Point) string {
	parts := []string{point.Type, point.Scope, point.Field, point.Status, point.Evidence}
	parts = append(parts, point.Keywords...)
	if point.Unit != nil {
		parts = append(parts, *point.Unit)
	}
	if encoded, err := json.Marshal(point.Value); err == nil {
		parts = append(parts, string(encoded))
	}
	return Normalize(strings.Join(parts, " "))
}

func authorisedPayloadText(raw string) string {
	var payload map[string]any
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return ""
	}
	keys := []string{"transcript_text", "message_text", "original_message", "evidence"}
	var values []string
	for _, key := range keys {
		if value, ok := payload[key].(string); ok {
			values = append(values, value)
		}
	}
	return strings.Join(values, " ")
}

func linkable(point Point) bool {
	return point.Field != "" && ((point.Scope == "profile") || (point.Scope == "thread" && point.ThreadID != nil))
}

func isExplicitUpdate(point Point) bool {
	return point.Type == "correction" || point.Type == "resolution" || point.Status == "superseded"
}

func sameRelationship(original, update Point) bool {
	if original.Field != update.Field || original.Scope != update.Scope {
		return false
	}
	if original.Scope == "profile" {
		return true
	}
	return original.ThreadID != nil && update.ThreadID != nil && *original.ThreadID == *update.ThreadID
}

func renderHistory(matches []Match, updates []RelatedUpdate) string {
	parts := make([]string, 0, len(matches)+len(updates))
	for _, match := range matches {
		line := fmt.Sprintf("%s, %s", displayDate(match.OccurredAt), strings.ReplaceAll(match.Channel, "_", " "))
		if match.Summary != "" {
			line += ": " + match.Summary
		}
		if len(match.SupportingEvidence) > 0 {
			line += " Evidence: " + match.SupportingEvidence[0]
		}
		line += " Source: " + match.SourceEventID + "."
		parts = append(parts, line)
	}
	for _, update := range updates {
		parts = append(parts, fmt.Sprintf("Later %s on %s: %s Evidence: %s Source: %s.", update.Point.Type,
			displayDate(update.OccurredAt), valueString(update.Point.Value), update.Point.Evidence, update.SourceEventID))
	}
	return strings.Join(parts, " ")
}

func displayDate(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return parsed.UTC().Format("2 Jan 2006")
}

func valueString(value any) string {
	if stringValue, ok := value.(string); ok {
		return stringValue
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func boundResponse(response *Response) {
	for {
		encoded, _ := json.Marshal(response)
		if len(encoded) <= maxResponseBytes {
			return
		}
		response.ResponseTruncated = true
		if len(response.RelatedUpdates) > 0 {
			response.RelatedUpdates = response.RelatedUpdates[:len(response.RelatedUpdates)-1]
			response.RelatedUpdatesLimited = true
		} else if len(response.Matches) > 1 {
			response.Matches = response.Matches[:len(response.Matches)-1]
			response.SearchScopeLimited = true
		} else {
			response.HistoryText = truncate(response.HistoryText, 500)
			return
		}
		response.HistoryText = renderHistory(response.Matches, response.RelatedUpdates)
	}
}

func Normalize(value string) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	for _, current := range value {
		if unicode.IsLetter(current) || unicode.IsNumber(current) || current == '_' {
			builder.WriteRune(current)
		} else {
			builder.WriteByte(' ')
		}
	}
	normalized := " " + strings.Join(strings.Fields(builder.String()), " ") + " "
	aliases := []struct{ from, to string }{
		{"minimum order", "minimum_order"}, {"catalog", "catalogue"}, {"कैटलॉग", "catalogue"},
		{"अपलोड", "upload"}, {"duplicates", "duplicate"}, {"डुप्लिकेट", "duplicate"},
		{"qty", "quantity"}, {"मात्रा", "quantity"}, {"price", "quoted_price"},
		{"rate", "quoted_price"}, {"कीमत", "quoted_price"}, {"दाम", "quoted_price"},
	}
	for _, alias := range aliases {
		normalized = strings.ReplaceAll(normalized, " "+alias.from+" ", " "+alias.to+" ")
	}
	return strings.TrimSpace(strings.Join(strings.Fields(normalized), " "))
}

func Terms(topic string) []string {
	normalized := Normalize(topic)
	filler := map[string]bool{"what": true, "when": true, "where": true, "which": true, "did": true,
		"i": true, "we": true, "you": true, "my": true, "our": true, "the": true, "a": true, "an": true,
		"about": true, "previous": true, "history": true, "please": true, "check": true, "tell": true, "me": true,
		"hai": true, "tha": true, "ka": true, "ki": true, "ke": true}
	seen := map[string]bool{}
	var result []string
	for _, term := range strings.Fields(normalized) {
		if filler[term] || term == "" || utf8.RuneCountInString(term) > 64 || seen[term] {
			continue
		}
		seen[term] = true
		result = append(result, term)
		if len(result) == 8 {
			break
		}
	}
	return result
}

func containsTerm(text, term string) bool {
	if text == "" || term == "" {
		return false
	}
	return strings.Contains(" "+text+" ", " "+term+" ")
}

func truncate(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes-1]) + "…"
}

func appendUnique(values []string, candidate string) []string {
	for _, existing := range values {
		if existing == candidate {
			return values
		}
	}
	return append(values, candidate)
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
