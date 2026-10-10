package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"vani-context/internal/storage"
	"vani-context/memorycontract"
)

type Builder struct {
	store         *storage.SQLiteStore
	recentDays    int
	tokenBudget   int
	referenceTime func() time.Time
}

type ProfileMemory struct {
	ProfileJSON      json.RawMessage `json:"profile_json"`
	BusinessName     *string         `json:"business_name"`
	City             *string         `json:"city"`
	ProfileUpdatedAt string          `json:"profile_updated_at"`
}

type Fact struct {
	Field       string   `json:"field"`
	Value       any      `json:"value"`
	Unit        *string  `json:"unit"`
	Status      string   `json:"status"`
	SourceIDs   []string `json:"source_ids"`
	EffectiveAt string   `json:"effective_at"`
	Evidence    string   `json:"evidence,omitempty"`
	Type        string   `json:"type"`
	ThreadID    *string  `json:"thread_id,omitempty"`
}

type Thread struct {
	ID             string   `json:"id"`
	Issue          string   `json:"issue"`
	State          string   `json:"state"`
	ConfirmedFacts []Fact   `json:"confirmed_facts"`
	MissingDetails []string `json:"missing_details"`
	NextStep       string   `json:"next_step,omitempty"`
	SourceIDs      []string `json:"source_ids"`
	EffectiveAt    string   `json:"effective_at"`
}

type RecentHistory struct {
	InteractionID     string  `json:"interaction_id"`
	OccurredAt        string  `json:"occurred_at"`
	Channel           string  `json:"channel"`
	ThreadID          *string `json:"thread_id,omitempty"`
	Summary           string  `json:"summary"`
	SourceID          string  `json:"source_id"`
	SummaryProvenance string  `json:"summary_provenance,omitempty"`
}

type BuildMetadata struct {
	ReferenceTime         string  `json:"reference_time"`
	RecentHistoryDays     int     `json:"recent_history_days"`
	InteractionsUpdatedAt *string `json:"interactions_updated_at"`
}

type SnapshotMemory struct {
	Profile             ProfileMemory     `json:"profile"`
	Preferences         map[string]Fact   `json:"preferences"`
	Restrictions        []Fact            `json:"restrictions"`
	OpenThreads         []Thread          `json:"open_threads"`
	Commitments         []Fact            `json:"commitments"`
	RecentHistory       []RecentHistory   `json:"recent_history"`
	UnresolvedConflicts []json.RawMessage `json:"unresolved_conflicts"`
	UnavailableFields   []string          `json:"unavailable_fields"`
	Build               BuildMetadata     `json:"build"`
}

type point = memorycontract.Point

func NewBuilder(store *storage.SQLiteStore, recentDays, tokenBudget int, referenceTime func() time.Time) *Builder {
	if referenceTime == nil {
		referenceTime = time.Now
	}
	return &Builder{store: store, recentDays: recentDays, tokenBudget: tokenBudget, referenceTime: referenceTime}
}

func (b *Builder) RebuildAll(ctx context.Context) error {
	keys, err := b.store.ListUserKeys(ctx)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := b.Rebuild(ctx, key); err != nil {
			return fmt.Errorf("rebuild %s/%s: %w", key.GLUSRID, key.Role, err)
		}
	}
	return nil
}

func (b *Builder) RebuildStale(ctx context.Context) (int, error) {
	keys, err := b.store.ListUserKeys(ctx)
	if err != nil {
		return 0, err
	}
	rebuilt := 0
	for _, key := range keys {
		stale, err := b.store.NeedsRebuild(ctx, key)
		if err != nil {
			return rebuilt, err
		}
		if stale {
			if err := b.Rebuild(ctx, key); err != nil {
				return rebuilt, fmt.Errorf("rebuild %s/%s: %w", key.GLUSRID, key.Role, err)
			}
			rebuilt++
		}
	}
	return rebuilt, nil
}

func (b *Builder) Rebuild(ctx context.Context, key storage.UserKey) error {
	input, err := b.store.LoadBuildInput(ctx, key.GLUSRID, key.Role)
	if err != nil {
		return err
	}
	reference := b.referenceTime().UTC()
	version := int64(1)
	if input.Existing != nil {
		version = input.Existing.Version + 1
	}
	memory, coverage, err := b.buildMemory(input, reference)
	if err != nil {
		return err
	}
	markdown, tokens := b.render(key, memory, version, reference)
	memoryJSON, err := json.Marshal(memory)
	if err != nil {
		return fmt.Errorf("encode snapshot memory: %w", err)
	}
	coverageJSON, err := json.Marshal(coverage)
	if err != nil {
		return fmt.Errorf("encode source coverage: %w", err)
	}
	return b.store.PublishSnapshot(ctx, key, storage.PublishedSnapshot{
		MemoryJSON:             memoryJSON,
		ContextMD:              markdown,
		TokenCount:             int64(tokens),
		TokenCountMethod:       "estimated",
		SourceCoverageJSON:     coverageJSON,
		LatestSourceEventAt:    input.LatestInteractionOccurredAt,
		ExpectedProfileUpdated: input.User.ProfileUpdatedAt,
		ExpectedInteractionMax: input.LatestInteractionUpdatedAt,
		UpdatedAt:              formatUTC(reference),
		Version:                version,
	})
}

func (b *Builder) buildMemory(input storage.BuildInput, reference time.Time) (SnapshotMemory, map[string]bool, error) {
	memory := SnapshotMemory{
		Profile: ProfileMemory{
			ProfileJSON:      append(json.RawMessage(nil), input.User.ProfileJSON...),
			BusinessName:     input.User.BusinessName,
			City:             input.User.City,
			ProfileUpdatedAt: input.User.ProfileUpdatedAt,
		},
		Preferences:         map[string]Fact{},
		Restrictions:        []Fact{},
		OpenThreads:         []Thread{},
		Commitments:         []Fact{},
		RecentHistory:       []RecentHistory{},
		UnresolvedConflicts: []json.RawMessage{},
		UnavailableFields:   []string{},
		Build: BuildMetadata{
			ReferenceTime:         formatUTC(reference),
			RecentHistoryDays:     b.recentDays,
			InteractionsUpdatedAt: input.LatestInteractionUpdatedAt,
		},
	}
	restrictions := factsByKey(memory.Restrictions)
	commitments := factsByKey(memory.Commitments)
	threads := threadsByID(memory.OpenThreads)
	cutoff := reference.AddDate(0, 0, -b.recentDays)
	coverage := map[string]bool{"profile": true}
	for index := len(input.Interactions) - 1; index >= 0; index-- {
		item := input.Interactions[index]
		coverage[item.Channel] = true
		occurred, err := time.Parse(time.RFC3339Nano, item.OccurredAt)
		if err != nil {
			return SnapshotMemory{}, nil, fmt.Errorf("interaction %s has invalid occurred_at: %w", item.InteractionID, err)
		}
		occurred = occurred.UTC()
		if !occurred.Before(cutoff) && !occurred.After(reference) && item.Summary != nil && strings.TrimSpace(*item.Summary) != "" {
			memory.RecentHistory = append(memory.RecentHistory, RecentHistory{
				InteractionID: item.InteractionID, OccurredAt: formatUTC(occurred), Channel: item.Channel,
				ThreadID: item.ThreadID, Summary: strings.TrimSpace(*item.Summary), SourceID: item.SourceEventID,
				SummaryProvenance: summaryProvenance(item.SourcePayloadJSON),
			})
		}
		var points []point
		if err := json.Unmarshal(item.ImportantPointsJSON, &points); err != nil {
			return SnapshotMemory{}, nil, fmt.Errorf("parse points for %s: %w", item.InteractionID, err)
		}
		for _, candidate := range points {
			fact, err := factFromPoint(candidate, item, occurred)
			if err != nil {
				return SnapshotMemory{}, nil, fmt.Errorf("point in %s: %w", item.InteractionID, err)
			}
			key := pointKey(candidate)
			switch candidate.Type {
			case "preference":
				if candidate.Scope == "profile" {
					if candidate.Status == "superseded" {
						delete(memory.Preferences, candidate.Field)
					} else {
						memory.Preferences[candidate.Field] = fact
					}
				}
			case "correction":
				if candidate.Scope == "profile" {
					if candidate.Status == "superseded" {
						delete(memory.Preferences, candidate.Field)
					} else {
						memory.Preferences[candidate.Field] = fact
					}
				} else {
					upsertThreadFact(threads, candidate, item, fact)
				}
			case "fact":
				if candidate.Scope == "thread" {
					upsertThreadFact(threads, candidate, item, fact)
				}
			case "restriction":
				if candidate.Status == "superseded" {
					delete(restrictions, key)
				} else {
					restrictions[key] = fact
				}
			case "issue":
				threadKey := resolvedThreadKey(candidate, item)
				threads[threadKey] = Thread{ID: threadKey, Issue: pointValueString(fact.Value), State: "open",
					ConfirmedFacts: []Fact{}, MissingDetails: []string{}, SourceIDs: fact.SourceIDs,
					EffectiveAt: fact.EffectiveAt}
			case "resolution":
				threadKey := resolvedThreadKey(candidate, item)
				delete(threads, threadKey)
				for commitmentKey, commitment := range commitments {
					if pointerValue(commitment.ThreadID) == threadKey && (candidate.Field == "other" || candidate.Field == "" || commitment.Field == candidate.Field) {
						delete(commitments, commitmentKey)
					}
				}
			case "commitment":
				key = candidate.Field + "|" + pointerValue(candidate.ThreadID)
				if candidate.Status == "superseded" {
					delete(commitments, key)
				} else {
					commitments[key] = fact
				}
			}
		}
	}
	sort.Slice(memory.RecentHistory, func(i, j int) bool { return memory.RecentHistory[i].OccurredAt > memory.RecentHistory[j].OccurredAt })
	memory.Restrictions = sortedFacts(restrictions)
	memory.Commitments = sortedFacts(commitments)
	memory.OpenThreads = sortedThreads(threads)
	var profileMarker struct {
		DataClassification string `json:"data_classification"`
	}
	if json.Unmarshal(input.User.ProfileJSON, &profileMarker) == nil && profileMarker.DataClassification == "synthetic_sample" {
		coverage["synthetic"] = true
	}
	return memory, coverage, nil
}

func factFromPoint(candidate point, item storage.InteractionRecord, occurred time.Time) (Fact, error) {
	if candidate.Field == "" || candidate.Type == "" || candidate.Status == "" || len(candidate.Value) == 0 {
		return Fact{}, fmt.Errorf("missing required structured fields")
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(candidate.Value)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return Fact{}, fmt.Errorf("invalid value: %w", err)
	}
	return Fact{Field: candidate.Field, Value: value, Unit: candidate.Unit, Status: candidate.Status,
		SourceIDs: []string{item.SourceEventID}, EffectiveAt: formatUTC(occurred),
		Evidence: candidate.Evidence, Type: candidate.Type, ThreadID: candidate.ThreadID}, nil
}

func upsertThreadFact(threads map[string]Thread, candidate point, item storage.InteractionRecord, fact Fact) {
	threadKey := resolvedThreadKey(candidate, item)
	thread, exists := threads[threadKey]
	if !exists {
		return
	}
	replaced := false
	for index := range thread.ConfirmedFacts {
		if thread.ConfirmedFacts[index].Field == fact.Field {
			thread.ConfirmedFacts[index] = fact
			replaced = true
			break
		}
	}
	if !replaced {
		thread.ConfirmedFacts = append(thread.ConfirmedFacts, fact)
	}
	thread.SourceIDs = appendUniqueString(thread.SourceIDs, item.SourceEventID)
	if fact.EffectiveAt > thread.EffectiveAt {
		thread.EffectiveAt = fact.EffectiveAt
	}
	threads[threadKey] = thread
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func summaryProvenance(raw json.RawMessage) string {
	var payload struct {
		Validation struct {
			SummaryProvenance string `json:"summary_provenance"`
		} `json:"validation"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	return payload.Validation.SummaryProvenance
}

func (b *Builder) render(key storage.UserKey, memory SnapshotMemory, version int64, reference time.Time) (string, int) {
	roleTitle := strings.ToUpper(key.Role[:1]) + key.Role[1:]
	lines := []string{fmt.Sprintf("# %s context", roleTitle), "GLID: " + key.GLUSRID,
		fmt.Sprintf("Version: %d", version), "Generated at: " + formatUTC(reference), ""}
	if isSynthetic(memory.Profile.ProfileJSON) {
		lines = append(lines, "> Synthetic sample data — not a real user.", "")
	}
	if memory.Profile.BusinessName != nil || memory.Profile.City != nil {
		lines = append(lines, "## Profile")
		parts := []string{}
		if memory.Profile.BusinessName != nil {
			parts = append(parts, compact(*memory.Profile.BusinessName))
		}
		if memory.Profile.City != nil {
			parts = append(parts, compact(*memory.Profile.City))
		}
		lines = append(lines, "- "+strings.Join(parts, ", ")+".", "")
	}
	lines = appendFactMap(lines, "Preferences", memory.Preferences)
	lines = appendFacts(lines, "Restrictions", memory.Restrictions)
	if len(memory.OpenThreads) > 0 {
		lines = append(lines, "## Open threads")
		for _, thread := range memory.OpenThreads {
			lines = append(lines, fmt.Sprintf("- [thread_id: %s] %s (open). Sources: %s.", compact(thread.ID), compact(thread.Issue), strings.Join(thread.SourceIDs, ", ")))
		}
		lines = append(lines, "")
	}
	lines = appendFacts(lines, "Commitments", memory.Commitments)
	base := strings.Join(lines, "\n")
	maxChars := b.tokenBudget * 4
	if len([]rune(base)) > maxChars {
		base = fitWholeLines(base, maxChars, "- Overflow: additional structured durable context is available in the API fields.")
	}
	if len(memory.RecentHistory) > 0 {
		heading := "## Recent conversations (last " + fmt.Sprint(b.recentDays) + " days)"
		candidate := strings.TrimRight(base, "\n") + "\n\n" + heading + "\n"
		included := 0
		for _, history := range memory.RecentHistory {
			label := ""
			if strings.HasPrefix(history.SummaryProvenance, "model_generated") {
				label = " (model-generated summary)"
			}
			line := fmt.Sprintf("- %s [%s]%s: %s Source: %s.\n", history.OccurredAt, history.Channel, label, compact(history.Summary), history.SourceID)
			if len([]rune(candidate+line)) > maxChars {
				break
			}
			candidate += line
			included++
		}
		if included < len(memory.RecentHistory) {
			overflow := fmt.Sprintf("- Overflow: %d additional recent conversation(s) omitted from this briefing.\n", len(memory.RecentHistory)-included)
			if len([]rune(candidate+overflow)) <= maxChars {
				candidate += overflow
			}
		}
		base = candidate
	}
	base = strings.TrimSpace(base)
	return base, (utf8.RuneCountInString(base) + 3) / 4
}

func appendFactMap(lines []string, heading string, facts map[string]Fact) []string {
	if len(facts) == 0 {
		return lines
	}
	keys := make([]string, 0, len(facts))
	for key := range facts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines = append(lines, "## "+heading)
	for _, key := range keys {
		fact := facts[key]
		lines = append(lines, fmt.Sprintf("- %s: %s%s. Sources: %s. Effective: %s.", key,
			compact(pointValueString(fact.Value)), unitSuffix(fact.Unit), strings.Join(fact.SourceIDs, ", "), fact.EffectiveAt))
	}
	return append(lines, "")
}

func appendFacts(lines []string, heading string, facts []Fact) []string {
	if len(facts) == 0 {
		return lines
	}
	lines = append(lines, "## "+heading)
	for _, fact := range facts {
		lines = append(lines, fmt.Sprintf("- %s%s. Sources: %s. Effective: %s.", compact(pointValueString(fact.Value)),
			unitSuffix(fact.Unit), strings.Join(fact.SourceIDs, ", "), fact.EffectiveAt))
	}
	return append(lines, "")
}

func factsByKey(facts []Fact) map[string]Fact {
	result := map[string]Fact{}
	for _, fact := range facts {
		result[fact.Field+"|"+pointerValue(fact.ThreadID)] = fact
	}
	return result
}

func threadsByID(threads []Thread) map[string]Thread {
	result := map[string]Thread{}
	for _, thread := range threads {
		result[thread.ID] = thread
	}
	return result
}

func sortedFacts(values map[string]Fact) []Fact {
	result := make([]Fact, 0, len(values))
	for _, fact := range values {
		result = append(result, fact)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].EffectiveAt == result[j].EffectiveAt {
			return pointValueString(result[i].Value) < pointValueString(result[j].Value)
		}
		return result[i].EffectiveAt > result[j].EffectiveAt
	})
	return result
}

func sortedThreads(values map[string]Thread) []Thread {
	result := make([]Thread, 0, len(values))
	for _, thread := range values {
		result = append(result, thread)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func pointKey(candidate point) string {
	return candidate.Type + "|" + candidate.Field + "|" + pointerValue(candidate.ThreadID) + "|" + string(candidate.Value)
}

func resolvedThreadKey(candidate point, item storage.InteractionRecord) string {
	if candidate.ThreadID != nil && *candidate.ThreadID != "" {
		return *candidate.ThreadID
	}
	if item.ThreadID != nil && *item.ThreadID != "" {
		return *item.ThreadID
	}
	return "unresolved:" + item.InteractionID + ":" + candidate.Field
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func pointValueString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
}

func unitSuffix(unit *string) string {
	if unit == nil || *unit == "" {
		return ""
	}
	return " " + compact(*unit)
}

func compact(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func fitWholeLines(value string, maxChars int, overflow string) string {
	lines := strings.Split(value, "\n")
	result := ""
	for _, line := range lines {
		candidate := result + line + "\n"
		if len([]rune(candidate+overflow)) > maxChars {
			break
		}
		result = candidate
	}
	return strings.TrimSpace(result+overflow+"\n") + "\n\n"
}

func isSynthetic(profile json.RawMessage) bool {
	var marker struct {
		DataClassification string `json:"data_classification"`
	}
	return json.Unmarshal(profile, &marker) == nil && marker.DataClassification == "synthetic_sample"
}

func formatUTC(value time.Time) string { return value.UTC().Format("2006-01-02T15:04:05.000Z") }
