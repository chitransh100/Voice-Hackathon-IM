#!/usr/bin/env python3
"""Idempotently seed or clean up the SYNTH-SELLER-MEMORY-002 database fixture."""

import argparse
import hashlib
import json
import sqlite3
from datetime import datetime
from pathlib import Path
from zoneinfo import ZoneInfo


GLUSR_ID = "SYNTH-SELLER-MEMORY-002"
ROLE = "seller"
FIXTURE_ID = "vani-memory-e2e-002"
REFERENCE_TIME = "2026-10-09T18:00:00.000Z"
UPDATED_AT = "2026-10-09T18:05:00.000Z"
TIMEZONE = ZoneInfo("Asia/Kolkata")


def utc(local_value: str) -> str:
    value = datetime.strptime(local_value, "%Y-%m-%d %H:%M:%S").replace(tzinfo=TIMEZONE)
    return value.astimezone(ZoneInfo("UTC")).strftime("%Y-%m-%dT%H:%M:%S.000Z")


def point(kind, scope, thread, field, value, status, keywords, evidence, unit=None):
    return {
        "type": kind,
        "scope": scope,
        "thread_id": thread,
        "field": field,
        "value": value,
        "unit": unit,
        "status": status,
        "keywords": keywords,
        "evidence": evidence,
    }


def interaction(suffix, local_time, channel, source, event_id, thread, summary, transcript, points, profile_changes=None):
    occurred_at = utc(local_time)
    points_json = json.dumps(points, ensure_ascii=False, separators=(",", ":"))
    enrichment = {
        "summary": summary,
        "important_points_json": points_json,
        "profile_updates": [],
    }
    payload = {
        "data_classification": "synthetic_sample",
        "fixture_id": FIXTURE_ID,
        "ingestion_mode": "database_only_fixture",
        "transcript_text": transcript,
        "enrichment_json": json.dumps(enrichment, ensure_ascii=False, separators=(",", ":")),
        "canonical_hash": hashlib.sha256((event_id + points_json + transcript).encode()).hexdigest(),
        "validation": {
            "contract": "memory_contract_v1",
            "evidence_checked": True,
            "validated_at": UPDATED_AT,
        },
        "profile_changes": profile_changes or [],
        "event_time": {
            "input_field": "fixture_local_datetime",
            "raw_value": local_time,
            "source_timezone": "Asia/Kolkata",
            "normalized_utc": occurred_at,
        },
    }
    return {
        "interaction_id": f"synth-memory-002-{suffix}",
        "channel": channel,
        "source": source,
        "source_event_id": event_id,
        "occurred_at": occurred_at,
        "thread_id": thread,
        "summary": summary,
        "points": points,
        "points_json": points_json,
        "payload_json": json.dumps(payload, ensure_ascii=False, separators=(",", ":")),
    }


def fixture_data():
    duplicate_thread = "memory-002-catalogue-duplicate"
    quantity_thread = "memory-002-quantity-enquiry"
    packaging_thread = "memory-002-packaging-damage"
    support_thread = "memory-002-catalogue-support"
    preferences_thread = "memory-002-lasting-preferences"
    temporary_thread = "memory-002-temporary-language"

    interactions = [
        interaction(
            "duplicate-report", "2026-08-15 11:00:00", "web_chat", "synthetic_web_chat_fixture",
            "memory-002-web-duplicate-report", duplicate_thread,
            "Seller reported duplicate catalogue entries after an upload.",
            "USER: My catalogue upload created duplicate entries for the same pumps.",
            [point("issue", "thread", duplicate_thread, "catalogue_upload", "Duplicate pump catalogue entries", "reported",
                   ["catalog duplicate", "catalogue duplicate", "duplicate entries"],
                   "My catalogue upload created duplicate entries for the same pumps.")],
        ),
        interaction(
            "duplicate-estimate", "2026-08-25 15:00:00", "voice", "synthetic_voice_fixture",
            "memory-002-voice-duplicate-estimate", duplicate_thread,
            "Support estimated 48 hours for the duplicate-entry issue; completion was not confirmed.",
            "USER: You estimated 48 hours to fix the duplicate catalogue entries.",
            [point("fact", "thread", duplicate_thread, "catalogue_upload", "Estimated resolution time: 48 hours", "reported",
                   ["catalog duplicate", "catalogue duplicate", "48 hour estimate"],
                   "You estimated 48 hours to fix the duplicate catalogue entries.", "hours")],
        ),
        interaction(
            "duplicate-resolution", "2026-08-30 16:30:00", "voice", "synthetic_voice_fixture",
            "memory-002-voice-duplicate-resolved", duplicate_thread,
            "Seller explicitly confirmed that the duplicate catalogue entries were removed.",
            "USER: The duplicate catalogue entries have been removed now.",
            [point("resolution", "thread", duplicate_thread, "catalogue_upload", "Duplicate catalogue entries removed", "confirmed",
                   ["catalog duplicate", "catalogue duplicate", "duplicate entries removed"],
                   "The duplicate catalogue entries have been removed now.")],
        ),
        interaction(
            "packaging-damage", "2026-09-04 12:00:00", "whatsapp", "synthetic_whatsapp_fixture",
            "memory-002-whatsapp-packaging-damage", packaging_thread,
            "Seller reported packaging damage; no resolution is recorded.",
            "USER: The last pump shipment arrived with damaged packaging.",
            [point("issue", "thread", packaging_thread, "other", "Pump shipment packaging damaged", "reported",
                   ["packaging damage", "damaged packaging", "pump shipment"],
                   "The last pump shipment arrived with damaged packaging.")],
        ),
        interaction(
            "lasting-preferences", "2026-09-25 18:00:00", "voice", "synthetic_voice_fixture",
            "memory-002-voice-lasting-preferences", preferences_thread,
            "Seller explicitly requested Hindi for future calls and calls after 17:00 Asia/Kolkata.",
            "USER: Please always speak with me in Hindi for future calls.\nUSER: Please call me after 17:00.",
            [
                point("preference", "profile", None, "other", "Hindi", "confirmed",
                      ["language", "preferred language", "Hindi"], "Please always speak with me in Hindi for future calls."),
                point("preference", "profile", None, "callback_after", "17:00", "confirmed",
                      ["callback", "call time", "after 17:00"], "Please call me after 17:00.", "Asia/Kolkata"),
            ],
            [
                {"path": "preferences.preferred_language", "value": "Hindi", "evidence_point_index": 0,
                 "evidence": "Please always speak with me in Hindi for future calls.", "status": "applied",
                 "effective_at": utc("2026-09-25 18:00:00"), "revision": 1, "previous_existed": False},
                {"path": "preferences.callback_after", "value": "17:00", "evidence_point_index": 1,
                 "evidence": "Please call me after 17:00.", "status": "applied",
                 "effective_at": utc("2026-09-25 18:00:00"), "revision": 1, "previous_existed": False},
            ],
        ),
        interaction(
            "quantity-initial", "2026-09-28 11:00:00", "web_chat", "synthetic_web_chat_fixture",
            "memory-002-web-quantity-20", quantity_thread,
            "Seller initially requested 20 industrial water pumps.",
            "USER: I need 20 industrial water pumps.",
            [point("fact", "thread", quantity_thread, "quantity", 20, "reported",
                   ["quantity", "industrial water pumps", "20 pumps"], "I need 20 industrial water pumps.", "pumps")],
        ),
        interaction(
            "quantity-correction", "2026-09-30 14:00:00", "whatsapp", "synthetic_whatsapp_fixture",
            "memory-002-whatsapp-quantity-12", quantity_thread,
            "Seller corrected the quantity in the same enquiry thread from 20 pumps to 12 pumps.",
            "USER: Correction: I need 12 pumps, not 20.",
            [point("correction", "thread", quantity_thread, "quantity", 12, "confirmed",
                   ["quantity", "industrial water pumps", "12 pumps"], "Correction: I need 12 pumps, not 20.", "pumps")],
        ),
        interaction(
            "temporary-language", "2026-10-02 10:30:00", "voice", "synthetic_voice_fixture",
            "memory-002-voice-temporary-english", temporary_thread,
            "Seller requested English for this call only; lasting Hindi preference remains unchanged.",
            "USER: Please speak English for this call only.",
            [point("preference", "thread", temporary_thread, "other", "English for this call only", "confirmed",
                   ["language", "temporary", "this call only"], "Please speak English for this call only.")],
        ),
        interaction(
            "catalogue-support", "2026-10-07 16:00:00", "web_chat", "synthetic_web_chat_fixture",
            "memory-002-web-catalogue-support", support_thread,
            "Seller requested pump catalogue-upload help and recorded a promise of a template; delivery is not confirmed.",
            "USER: I need help uploading my industrial pump catalogue.\nUSER: You promised to send me the pump catalogue template.",
            [
                point("issue", "thread", support_thread, "catalogue_upload", "Needs help uploading industrial pump catalogue", "reported",
                      ["catalogue upload", "pump catalogue", "upload help"], "I need help uploading my industrial pump catalogue."),
                point("commitment", "thread", support_thread, "catalogue_upload", "Send pump catalogue template", "confirmed",
                      ["catalogue template", "pump catalogue", "promised template"], "You promised to send me the pump catalogue template."),
            ],
        ),
    ]

    profile = {
        "data_classification": "synthetic_sample",
        "fixture_id": FIXTURE_ID,
        "description": "Synthetic seller fixture for VANI end-to-end memory testing.",
        "business": {"type": "industrial water pumps", "synthetic": True},
        "categories": [{
            "id": "SYN-PUMP-INDUSTRIAL",
            "name": "Industrial water pumps",
            "details": {"seller_type": "manufacturer and supplier", "synthetic": True},
        }],
        "verification": {
            "kyc": {"status": "not_verified", "synthetic": True, "note": "No real KYC was performed for this fixture."},
            "metadata": {"source": "synthetic_fixture", "verified_as_real_business": False},
        },
        "engagement": {
            "lifecycle_stage": "synthetic_test",
            "available_channels": ["voice", "whatsapp", "web_chat"],
            "created_by": FIXTURE_ID,
        },
        "preferences": {
            "preferred_language": "Hindi",
            "callback_after": "17:00",
            "timezone": "Asia/Kolkata",
        },
        "unrelated_nested_fixture": {"preserve_me": True, "labels": ["memory", "e2e"]},
    }
    return profile, interactions


def fact(point_value, interaction_value):
    return {
        "field": point_value["field"], "value": point_value["value"], "unit": point_value["unit"],
        "status": point_value["status"], "source_ids": [interaction_value["source_event_id"]],
        "effective_at": interaction_value["occurred_at"], "evidence": point_value["evidence"],
        "type": point_value["type"], "thread_id": point_value["thread_id"],
    }


def snapshot(profile, interactions):
    by_suffix = {item["interaction_id"].removeprefix("synth-memory-002-"): item for item in interactions}
    preference_call = by_suffix["lasting-preferences"]
    preferences = {
        "other": fact(preference_call["points"][0], preference_call),
        "callback_after": fact(preference_call["points"][1], preference_call),
    }
    packaging = by_suffix["packaging-damage"]
    catalogue = by_suffix["catalogue-support"]
    open_threads = [
        {"id": packaging["thread_id"], "issue": packaging["points"][0]["value"], "state": "open",
         "confirmed_facts": [], "missing_details": [], "source_ids": [packaging["source_event_id"]],
         "effective_at": packaging["occurred_at"]},
        {"id": catalogue["thread_id"], "issue": catalogue["points"][0]["value"], "state": "open",
         "confirmed_facts": [], "missing_details": [], "source_ids": [catalogue["source_event_id"]],
         "effective_at": catalogue["occurred_at"]},
    ]
    recent_cutoff = "2026-09-24T18:00:00.000Z"
    recent = [{
        "interaction_id": item["interaction_id"], "occurred_at": item["occurred_at"], "channel": item["channel"],
        "thread_id": item["thread_id"], "summary": item["summary"], "source_id": item["source_event_id"],
    } for item in sorted(interactions, key=lambda value: value["occurred_at"], reverse=True)
      if recent_cutoff <= item["occurred_at"] <= REFERENCE_TIME]
    commitment = fact(catalogue["points"][1], catalogue)
    memory = {
        "profile": {"profile_json": profile, "business_name": "Synthetic Surya Pumps", "city": "Lucknow",
                    "profile_updated_at": preference_call["occurred_at"]},
        "preferences": preferences,
        "restrictions": [],
        "open_threads": sorted(open_threads, key=lambda value: value["id"]),
        "commitments": [commitment],
        "recent_history": recent,
        "unresolved_conflicts": [],
        "unavailable_fields": [],
        "build": {"reference_time": REFERENCE_TIME, "recent_history_days": 15, "interactions_updated_at": UPDATED_AT},
    }
    lines = [
        "# Seller context", f"GLID: {GLUSR_ID}", "Version: 1", f"Generated at: {REFERENCE_TIME}", "",
        "> Synthetic sample data — not a real user.", "", "## Profile", "- Synthetic Surya Pumps, Lucknow.", "",
        "## Preferences",
        f"- callback_after: 17:00 (Asia/Kolkata). Sources: {preference_call['source_event_id']}. Effective: {preference_call['occurred_at']}.",
        f"- other: Hindi. Sources: {preference_call['source_event_id']}. Effective: {preference_call['occurred_at']}.", "",
        "## Open threads",
    ]
    for thread in memory["open_threads"]:
        lines.append(f"- {thread['issue']} (open). Sources: {', '.join(thread['source_ids'])}.")
    lines += ["", "## Commitments",
              f"- Send pump catalogue template. Sources: {catalogue['source_event_id']}. Effective: {catalogue['occurred_at']}.", "",
              "## Recent conversations (last 15 days)"]
    for item in recent:
        lines.append(f"- {item['occurred_at']} [{item['channel']}]: {item['summary']} Source: {item['source_id']}.")
    context_md = "\n".join(lines).strip()
    coverage = {"profile": True, "synthetic": True, "voice": True, "whatsapp": True, "web_chat": True}
    return memory, context_md, coverage


def seed(connection):
    profile, interactions = fixture_data()
    existing = connection.execute("SELECT profile_json FROM users WHERE glusr_id=? AND role=?", (GLUSR_ID, ROLE)).fetchone()
    if existing:
        try:
            marker = json.loads(existing[0]).get("fixture_id")
        except (ValueError, TypeError):
            marker = None
        if marker != FIXTURE_ID:
            raise RuntimeError("refusing to overwrite an existing non-fixture user")
    collision_ids = [item["interaction_id"] for item in interactions]
    placeholders = ",".join("?" for _ in collision_ids)
    collisions = connection.execute(
        f"SELECT interaction_id, glusr_id, role FROM interactions WHERE interaction_id IN ({placeholders}) AND (glusr_id<>? OR role<>?)",
        (*collision_ids, GLUSR_ID, ROLE),
    ).fetchall()
    if collisions:
        raise RuntimeError(f"refusing to overwrite interaction collisions: {collisions}")

    profile_json = json.dumps(profile, ensure_ascii=False, separators=(",", ":"))
    profile_updated_at = utc("2026-09-25 18:00:00")
    connection.execute(
        """INSERT INTO users(glusr_id,role,business_name,city,profile_json,profile_updated_at)
           VALUES(?,?,?,?,?,?)
           ON CONFLICT(glusr_id,role) DO UPDATE SET business_name=excluded.business_name,city=excluded.city,
           profile_json=excluded.profile_json,profile_updated_at=excluded.profile_updated_at""",
        (GLUSR_ID, ROLE, "Synthetic Surya Pumps", "Lucknow", profile_json, profile_updated_at),
    )
    for item in interactions:
        connection.execute(
            """INSERT INTO interactions(
               interaction_id,glusr_id,role,channel,source,source_event_id,source_revision,occurred_at,received_at,
               updated_at,session_state,thread_id,summary,important_points_json,source_payload_json,
               processing_status,processed_revision,retry_count,next_retry_at,last_error)
               VALUES(?,?,?,?,?,?,1,?,?,?,'completed',?,?,?,?, 'processed',1,0,NULL,NULL)
               ON CONFLICT(interaction_id) DO UPDATE SET channel=excluded.channel,source=excluded.source,
               source_event_id=excluded.source_event_id,source_revision=1,occurred_at=excluded.occurred_at,
               received_at=excluded.received_at,updated_at=excluded.updated_at,session_state='completed',
               thread_id=excluded.thread_id,summary=excluded.summary,important_points_json=excluded.important_points_json,
               source_payload_json=excluded.source_payload_json,processing_status='processed',processed_revision=1,
               retry_count=0,next_retry_at=NULL,last_error=NULL""",
            (item["interaction_id"], GLUSR_ID, ROLE, item["channel"], item["source"], item["source_event_id"],
             item["occurred_at"], UPDATED_AT, UPDATED_AT, item["thread_id"], item["summary"],
             item["points_json"], item["payload_json"]),
        )
    memory, context_md, coverage = snapshot(profile, interactions)
    memory_json = json.dumps(memory, ensure_ascii=False, separators=(",", ":"))
    coverage_json = json.dumps(coverage, separators=(",", ":"))
    token_count = (len(context_md) + 3) // 4
    latest_event = max(item["occurred_at"] for item in interactions)
    connection.execute(
        """INSERT INTO current_context(glusr_id,role,memory_json,context_md,version,updated_at,
           latest_source_event_at,token_count,token_count_method,source_coverage_json)
           VALUES(?,?,?,?,1,?,?,?,'estimated',?)
           ON CONFLICT(glusr_id,role) DO UPDATE SET memory_json=excluded.memory_json,context_md=excluded.context_md,
           version=1,updated_at=excluded.updated_at,latest_source_event_at=excluded.latest_source_event_at,
           token_count=excluded.token_count,token_count_method=excluded.token_count_method,
           source_coverage_json=excluded.source_coverage_json""",
        (GLUSR_ID, ROLE, memory_json, context_md, REFERENCE_TIME, latest_event, token_count, coverage_json),
    )


def cleanup(connection):
    existing = connection.execute("SELECT profile_json FROM users WHERE glusr_id=? AND role=?", (GLUSR_ID, ROLE)).fetchone()
    if not existing:
        return
    try:
        marker = json.loads(existing[0]).get("fixture_id")
    except (ValueError, TypeError):
        marker = None
    if marker != FIXTURE_ID:
        raise RuntimeError("refusing to delete an existing non-fixture user")
    connection.execute("DELETE FROM current_context WHERE glusr_id=? AND role=?", (GLUSR_ID, ROLE))
    connection.execute("DELETE FROM interactions WHERE glusr_id=? AND role=?", (GLUSR_ID, ROLE))
    connection.execute("DELETE FROM users WHERE glusr_id=? AND role=?", (GLUSR_ID, ROLE))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=("seed", "cleanup"))
    parser.add_argument("--db", default=str(Path(__file__).resolve().parents[1] / "data" / "vani.sqlite"))
    args = parser.parse_args()
    connection = sqlite3.connect(args.db, timeout=10)
    try:
        connection.execute("PRAGMA foreign_keys=ON")
        connection.execute("BEGIN IMMEDIATE")
        seed(connection) if args.action == "seed" else cleanup(connection)
        connection.commit()
    except Exception:
        connection.rollback()
        raise
    finally:
        connection.close()
    print(f"{args.action} complete for {GLUSR_ID}/{ROLE}")


if __name__ == "__main__":
    main()
