package storage

import (
	"context"
	"encoding/json"
	"fmt"
)

// SeedSynthetic inserts only visibly synthetic demo fixtures. It is never run
// automatically by the server. The memory builder publishes snapshots from
// these source records.
func (s *SQLiteStore) SeedSynthetic(ctx context.Context) error {
	alpha, jaipur := "Synthetic Alpha Pumps", "Jaipur"
	beta, surat := "Synthetic Beta Textiles", "Surat"
	users := []UserRecord{
		{
			GLUSRID: "SYNTH-SELLER-001", Role: "seller", BusinessName: &alpha, City: &jaipur,
			ProfileUpdatedAt: "2026-10-08T11:45:00.000Z",
			ProfileJSON: json.RawMessage(`{
  "data_classification": "synthetic_sample",
  "description": "Industrial pump supplier (synthetic test fixture)",
  "categories": [{"id": "SYN-PUMP", "name": "Industrial pumps"}],
  "verification": {
    "kyc": {
      "status": "sample_verified",
      "source": {"system": "synthetic_fixture", "observed_at": "2026-10-01T08:00:00.000Z"}
    }
  },
  "engagement": {"last_90_days": {"leads_received": 4, "channels": ["voice", "web_chat", "whatsapp"]}}
}`),
		},
		{
			GLUSRID: "SYNTH-SELLER-002", Role: "seller", BusinessName: &beta, City: &surat,
			ProfileUpdatedAt: "2026-10-08T11:46:00.000Z",
			ProfileJSON:      json.RawMessage(`{"data_classification":"synthetic_sample","description":"Textile supplier (synthetic test fixture)","categories":[{"id":"SYN-TEXTILE","name":"Textiles"}],"verification":{"kyc":{"status":"sample_pending","source":{"system":"synthetic_fixture"}}}}`),
		},
		{
			GLUSRID: "SYNTH-SELLER-001", Role: "buyer", BusinessName: &alpha, City: &jaipur,
			ProfileUpdatedAt: "2026-10-08T11:47:00.000Z",
			ProfileJSON:      json.RawMessage(`{"data_classification":"synthetic_sample","buyer_profile":{"categories_searched":["synthetic motors"],"kyc_status":"sample_unknown"}}`),
		},
	}
	for _, user := range users {
		if err := s.UpsertUser(ctx, user); err != nil {
			return fmt.Errorf("seed synthetic user %s/%s: %w", user.GLUSRID, user.Role, err)
		}
	}
	thread := "synthetic-catalogue-42"
	interactions := []struct {
		glusrID, role string
		item          InteractionRecord
	}{
		{"SYNTH-SELLER-001", "seller", InteractionRecord{
			InteractionID: "synthetic-call-101", Channel: "voice", Source: "synthetic_fixture", SourceEventID: "synthetic-call-101", SourceRevision: 1,
			OccurredAt: "2026-10-07T12:00:00.000Z", UpdatedAt: "2026-10-07T12:05:00.000Z", ThreadID: &thread,
			Summary:             stringPtr("Seller asked to continue the catalogue discussion by voice and requested calls after 5 PM IST."),
			ImportantPointsJSON: json.RawMessage(`[{"type":"preference","scope":"profile","thread_id":null,"field":"callback_after","value":"17:00","unit":"Asia/Kolkata","status":"confirmed","keywords":["callback","after 5 pm"],"evidence":"Please call me after 5 PM."}]`),
		}},
		{"SYNTH-SELLER-001", "seller", InteractionRecord{
			InteractionID: "synthetic-whatsapp-201", Channel: "whatsapp", Source: "synthetic_fixture", SourceEventID: "synthetic-whatsapp-201", SourceRevision: 1,
			OccurredAt: "2026-10-01T09:30:00.000Z", UpdatedAt: "2026-10-01T09:31:00.000Z", ThreadID: &thread,
			Summary: stringPtr("Seller shared a synthetic catalogue screenshot over WhatsApp."), ImportantPointsJSON: json.RawMessage(`[]`),
		}},
		{"SYNTH-SELLER-001", "seller", InteractionRecord{
			InteractionID: "synthetic-boundary-in", Channel: "web_chat", Source: "synthetic_fixture", SourceEventID: "synthetic-boundary-in", SourceRevision: 1,
			OccurredAt: "2026-09-23T12:01:00.000Z", UpdatedAt: "2026-09-23T12:02:00.000Z", ThreadID: &thread,
			Summary: stringPtr("Interaction exactly on the inclusive 15-day boundary."), ImportantPointsJSON: json.RawMessage(`[]`),
		}},
		{"SYNTH-SELLER-001", "seller", InteractionRecord{
			InteractionID: "synthetic-boundary-out", Channel: "web_chat", Source: "synthetic_fixture", SourceEventID: "synthetic-boundary-out", SourceRevision: 1,
			OccurredAt: "2026-09-23T12:00:59.999Z", UpdatedAt: "2026-09-23T12:00:59.999Z", ThreadID: &thread,
			Summary: stringPtr("Interaction one millisecond before the 15-day boundary."), ImportantPointsJSON: json.RawMessage(`[]`),
		}},
		{"SYNTH-SELLER-001", "seller", InteractionRecord{
			InteractionID: "synthetic-old-issue", Channel: "web_chat", Source: "synthetic_fixture", SourceEventID: "synthetic-chat-202", SourceRevision: 1,
			OccurredAt: "2026-09-01T10:00:00.000Z", UpdatedAt: "2026-09-01T10:01:00.000Z", ThreadID: &thread,
			Summary:             stringPtr("Seller requested catalogue upload assistance."),
			ImportantPointsJSON: json.RawMessage(`[{"type":"issue","scope":"thread","thread_id":"synthetic-catalogue-42","field":"catalogue_upload","value":"Catalogue upload assistance requested","unit":null,"status":"confirmed","keywords":["catalogue","upload"],"evidence":"I still need help uploading the catalogue."}]`),
		}},
		{"SYNTH-SELLER-001", "seller", InteractionRecord{
			InteractionID: "synthetic-old-commitment", Channel: "voice", Source: "synthetic_fixture", SourceEventID: "synthetic-call-090", SourceRevision: 1,
			OccurredAt: "2026-09-02T08:00:00.000Z", UpdatedAt: "2026-09-02T08:02:00.000Z", ThreadID: &thread,
			Summary:             stringPtr("An older unresolved synthetic commitment was recorded."),
			ImportantPointsJSON: json.RawMessage(`[{"type":"commitment","scope":"thread","thread_id":"synthetic-catalogue-42","field":"catalogue_upload","value":"Provide the missing catalogue template","unit":null,"status":"confirmed","keywords":["catalogue","template"],"evidence":"Please send me the missing catalogue template."}]`),
		}},
		{"SYNTH-SELLER-001", "seller", InteractionRecord{
			InteractionID: "synthetic-old-restriction", Channel: "voice", Source: "synthetic_fixture", SourceEventID: "synthetic-call-089", SourceRevision: 1,
			OccurredAt: "2026-08-31T08:00:00.000Z", UpdatedAt: "2026-08-31T08:02:00.000Z",
			Summary:             stringPtr("An older synthetic contact restriction was recorded."),
			ImportantPointsJSON: json.RawMessage(`[{"type":"restriction","scope":"profile","thread_id":null,"field":"callback_after","value":"Do not call before 17:00","unit":"Asia/Kolkata","status":"confirmed","keywords":["do not call","before 5 pm"],"evidence":"Do not call before 5 PM."}]`),
		}},
		{"SYNTH-SELLER-002", "seller", InteractionRecord{
			InteractionID: "synthetic-beta-call", Channel: "voice", Source: "synthetic_fixture", SourceEventID: "synthetic-beta-call", SourceRevision: 1,
			OccurredAt: "2026-10-06T08:00:00.000Z", UpdatedAt: "2026-10-06T08:01:00.000Z",
			Summary: stringPtr("Synthetic textile seller confirmed profile details."), ImportantPointsJSON: json.RawMessage(`[]`),
		}},
		{"SYNTH-SELLER-001", "buyer", InteractionRecord{
			InteractionID: "synthetic-buyer-enquiry", Channel: "enquiry", Source: "synthetic_fixture", SourceEventID: "synthetic-buyer-enquiry", SourceRevision: 1,
			OccurredAt: "2026-10-02T07:00:00.000Z", UpdatedAt: "2026-10-02T07:01:00.000Z",
			Summary: stringPtr("Synthetic buyer searched for motors."), ImportantPointsJSON: json.RawMessage(`[]`),
		}},
	}
	for _, fixture := range interactions {
		if err := s.UpsertInteraction(ctx, fixture.glusrID, fixture.role, fixture.item); err != nil {
			return fmt.Errorf("seed synthetic interaction %s: %w", fixture.item.InteractionID, err)
		}
	}
	return nil
}

func stringPtr(value string) *string { return &value }
