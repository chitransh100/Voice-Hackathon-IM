# Build journey, tools and lessons

[← Back to README](README.md) · [Impact and evidence](impact.md)

## How the solution evolved

We began with the Context API and the authoritative three-table migration. The first milestone was a deterministic compact snapshot that could return a complete profile, current preferences and recent cross-channel context with strict buyer/seller isolation.

The project stayed focused on Problem 2: create continuity without putting an LLM or an unbounded history scan on the conversation-start path. We used a prepared snapshot for the common case and keyword evidence lookup for older detail.

Next, we added bounded historical lookup for details intentionally absent from the compact context. The lookup path stayed read-only and used the same structured point format, thread relationships and source evidence.

The Profile API provided a clean pre-conversation read, while Enrichment completed the feedback loop by saving post-call transcripts, validating Sarvam extraction output, applying supported profile updates and rebuilding the snapshot.

The most important work happened during live integration. Real Sarvam payloads differed from simplified examples: source names varied, event IDs contained `/`, start timestamps lacked a timezone, transcript values could be structured or translated, numeric fields arrived as strings, and extraction output could contain invalid quotations or Python-style single-quoted objects. Each failure was converted into a contract or recovery test rather than handled as a one-off patch.

## Tools used

- **Go 1.26** — all four HTTP services, validation, workers and tests.
- **`net/http`** — explicit endpoints, timeouts, bounded request bodies and middleware.
- **SQLite via `modernc.org/sqlite`** — portable shared storage without CGO or an external server.
- **Go testing + `httptest`** — isolated temporary databases and HTTP contract tests.
- **Shell, curl and Python's read-only SQLite support** — local smoke tests and audit inspection. Python was not part of the runtime APIs.
- **Cloudflare Quick Tunnels** — temporary HTTPS access for the recorded Sarvam integration demo.
- **Structured JSON logs** — method, path, status and duration without request bodies, Authorization headers or response bodies.
- **Sarvam dashboard** — start-context loading, mid-call history tool and on-end enrichment mapping.
- **Codex collaboration** — code inspection, implementation, test execution, debugging and submission packaging.

## Integration problems and fixes

### Port conflict and stale processes

An early restart attempted to bind a port already in use. We identified and stopped the previous process before restarting each API and its tunnel separately.

**Lesson:** treat local API processes and tunnel processes as separate lifecycle units; verify `/health` before updating dashboard URLs.

### Lookup numeric input arrived as text

Sarvam sent `"90"` for `lookback_days`, while the initial handler expected a JSON number.

**Fix:** accept either an integer or integer text and normalize before validation. Empty strings remain invalid rather than silently expanding the search.

### Source, channel and event identifiers differed from examples

Real inputs used `Sarwam`, `Voice`, and Interaction IDs containing `/` and `:`.

**Fix:** preserve original source/channel provenance while canonicalizing source and channel; allow the documented provider identifier characters; retain immutable call metadata across revisions.

### Sarvam start time had no timezone

The real `start_datetime` was timezone-less, but storage requires UTC RFC3339.

**Fix:** require an explicitly configured workflow timezone (`Asia/Kolkata` in the demo), retain the raw value and timezone in provenance, and store normalized UTC. Receipt time is never substituted for call time.

### Invalid evidence discarded useful conversations

Initially, a point whose evidence was not an exact transcript excerpt caused the entire request to fail before the transcript was stored.

**Fix:** split strict envelope validation from extraction validation. Persist raw transcript/extraction first, filter invalid points individually, preserve original point indices and skip dependent profile changes explicitly.

### Translated transcript versus original-language quotation

In a real Hindi call, Sarvam extracted a Hindi quotation but the on-end transcript delivered only English-normalized caller text.

**Fix:** support structured turns containing both `en_text` and `indic_text`, preserve both, and validate against an exact caller variant. The extraction prompt now forbids translation or paraphrase. Historical calls with sufficient retained evidence were repaired through normal revisions.

### Missing thread IDs prevented resolutions

An extraction stated that a login issue was resolved but supplied no thread ID, so it could not safely close the correct issue.

**Fix:** render exact thread IDs in Context, require an owned thread ID for correction/resolution, generate deterministic IDs only for genuinely new issues, and keep unrelated threads separate.

### Malformed single-quoted extraction

Some real on-end outputs resembled Python dictionaries with single quotes instead of JSON.

**Fix:** retain the raw call, return HTTP `202` with an explicit validation error, keep rejected content outside trusted memory, and permit a corrected revision without duplicating the interaction.

### Concurrent writes and context rebuilds

Independent calls can update different profile preferences or race during snapshot publication.

**Fix:** use immediate transactions, compare profile timestamps before update, preserve unrelated nested fields, mark recoverable rebuild failures and complete them through worker retry or idempotent replay.

### Fixture continuity was not a live two-channel demo

The synthetic seller snapshot combines voice, WhatsApp and web-chat records and proves that the shared builder keeps sources and threads together. The recorded integration used Sarvam voice plus the HTTP APIs; it did not include a real WhatsApp/web-chat writer followed by voice.

**Lesson:** label fixture, automated and live evidence separately. Seeded channel labels cannot be presented as proof of deployed channel integrations.

## What we learned

1. A green dashboard label or HTTP `200` is not enough; verification must inspect stored points, profile decisions, thread state and the next Context output.
2. Raw conversation retention and trusted-memory publication are different responsibilities and should have different validation boundaries.
3. Exact evidence matching is useful only when the extractor and webhook receive the same transcript representation.
4. Thread IDs are essential for safely applying corrections and resolutions across channels.
5. Compact context and history lookup solve different problems: one optimizes every call, the other retrieves exceptional detail.
6. Idempotency must cover side effects such as profile updates and context versions, not only duplicate rows.
7. Temporary demo hosting should never be described as durable deployment.
8. Backend bilingual parsing tests do not establish live Hindi/Hinglish voice quality; transcript delivery and audio behavior require a real-call evaluation.

## Remaining work beyond the hackathon

- Add an authorised speaker-aware contract for assistant commitments if the product wants them in trusted memory.
- Add a durable call-session identity binding store for production inbound/outbound workflows.
- Replace Quick Tunnels with stable managed hosting and operational monitoring.
- Test the final published Sarvam agent version with a fresh call that delivers both original and translated transcript variants.
- Evaluate a production tokenizer and database strategy if the system is scaled beyond a single host.
- Run a true two-channel scenario through a production writer and the final published voice agent.
