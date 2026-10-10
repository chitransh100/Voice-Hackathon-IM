package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type fixture struct {
	id, channel, sourceID, occurred, thread, summary, points string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seed-synthetic:", err)
		os.Exit(1)
	}
}

func run() error {
	path := os.Getenv("VANI_DB_PATH")
	if path == "" {
		path = "../Context/data/vani.sqlite"
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", absolute)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return err
	}
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users
WHERE glusr_id = 'SYNTH-SELLER-001' AND role = 'seller'`).Scan(&exists); err != nil {
		return err
	}
	if exists != 1 {
		return fmt.Errorf("synthetic Context user is missing; run Context seed-synthetic first")
	}
	items := []fixture{
		{
			id: "lookup-demo-old-duplicate", channel: "web_chat", sourceID: "lookup-demo-chat-duplicate",
			occurred: "2026-08-10T10:00:00.000Z", thread: "lookup-demo-catalogue-42",
			summary: "Synthetic seller reported duplicate products after catalogue upload.",
			points:  `[{"type":"issue","scope":"thread","thread_id":"lookup-demo-catalogue-42","field":"catalogue_upload","value":"Duplicate products appeared","unit":null,"status":"reported","keywords":["catalogue","upload","duplicate"],"evidence":"My catalogue upload created duplicate products."}]`,
		},
		{
			id: "lookup-demo-resolution", channel: "voice", sourceID: "lookup-demo-call-resolution",
			occurred: "2026-09-01T09:00:00.000Z", thread: "lookup-demo-catalogue-42",
			summary: "Synthetic seller confirmed the duplicate catalogue issue was resolved.",
			points:  `[{"type":"resolution","scope":"thread","thread_id":"lookup-demo-catalogue-42","field":"catalogue_upload","value":"Duplicate products removed","unit":null,"status":"confirmed","keywords":["catalogue","duplicate","resolved"],"evidence":"The duplicate catalogue products are removed now."}]`,
		},
		{
			id: "lookup-demo-thread-a", channel: "enquiry", sourceID: "lookup-demo-enquiry-a",
			occurred: "2026-09-15T09:00:00.000Z", thread: "lookup-demo-pump-a",
			summary: "Synthetic pump enquiry A requested 100 pieces.",
			points:  `[{"type":"fact","scope":"thread","thread_id":"lookup-demo-pump-a","field":"quantity","value":100,"unit":"pieces","status":"confirmed","keywords":["pump","quantity"],"evidence":"For enquiry A, I need 100 pump pieces."}]`,
		},
		{
			id: "lookup-demo-thread-a-correction", channel: "voice", sourceID: "lookup-demo-enquiry-a-correction",
			occurred: "2026-09-20T09:00:00.000Z", thread: "lookup-demo-pump-a",
			summary: "Synthetic seller corrected pump enquiry A to 200 pieces.",
			points:  `[{"type":"correction","scope":"thread","thread_id":"lookup-demo-pump-a","field":"quantity","value":200,"unit":"pieces","status":"confirmed","keywords":["pump","quantity"],"evidence":"Actually, make enquiry A 200 pump pieces."}]`,
		},
		{
			id: "lookup-demo-thread-b", channel: "enquiry", sourceID: "lookup-demo-enquiry-b",
			occurred: "2026-09-16T09:00:00.000Z", thread: "lookup-demo-pump-b",
			summary: "Synthetic pump enquiry B requested 500 pieces.",
			points:  `[{"type":"fact","scope":"thread","thread_id":"lookup-demo-pump-b","field":"quantity","value":500,"unit":"pieces","status":"confirmed","keywords":["pump","quantity"],"evidence":"For enquiry B, I need 500 pump pieces."}]`,
		},
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range items {
		_, err := tx.ExecContext(ctx, `INSERT INTO interactions
(interaction_id, glusr_id, role, channel, source, source_event_id, source_revision,
 occurred_at, received_at, updated_at, session_state, thread_id, summary,
 important_points_json, source_payload_json, processing_status, processed_revision)
VALUES (?, 'SYNTH-SELLER-001', 'seller', ?, 'synthetic_lookup_fixture', ?, 1,
 ?, ?, ?, 'completed', ?, ?, ?, '{}', 'processed', 1)
ON CONFLICT(interaction_id) DO UPDATE SET occurred_at=excluded.occurred_at,
updated_at=excluded.updated_at, thread_id=excluded.thread_id, summary=excluded.summary,
important_points_json=excluded.important_points_json`, item.id, item.channel, item.sourceID,
			item.occurred, item.occurred, item.occurred, item.thread, item.summary, item.points)
		if err != nil {
			return fmt.Errorf("insert %s: %w", item.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Printf("inserted %d clearly marked synthetic lookup interactions into %s\n", len(items), absolute)
	return nil
}
