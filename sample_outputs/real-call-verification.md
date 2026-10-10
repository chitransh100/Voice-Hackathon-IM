# Real Sarvam-call verification

This is separated from automated test coverage to avoid overstating integration confidence.

## Verified with actual calls

- Sarvam's start workflow reached `GET /v1/context`; observed responses were HTTP `200`.
- Sarvam's on-end workflow reached `POST /interactions/enrich` with Interaction ID, start time, transcript and extraction values.
- Accepted calls were stored and marked `processed`.
- Malformed nested extraction returned HTTP `202` and remained stored with raw transcript/extraction and validation provenance.
- A short actual call with only identity confirmation produced a summary and `important_points_json: []`; no unsupported facts were invented.
- Lookup reached the API. A request with blank topic/lookback correctly returned HTTP `400`; the same public endpoint returned HTTP `200` when called with `topic_keyword="login issue"` and `lookback_days="90"`.

## Actual integration failures observed

- Some output-variable values used Python-style single quotes rather than strict JSON. Those calls were retained but not promoted to trusted memory.
- One Hindi call produced Hindi evidence while the delivered transcript contained only an English-normalized variant, so exact evidence validation correctly excluded the point.
- Some calls captured assistant speech only or ended before a substantive caller response; these produced no important points.

## Repaired through normal revisions

Where the full transcript and extraction were retained, synthetic historical calls were repaired by submitting corrected authenticated revisions using exact retained caller quotations and owned thread IDs. No duplicate interaction rows were created.

## Still not independently verified

- That the final published Sarvam version always delivers both original-language and translated transcript variants.
- Stable production hosting. The recorded demo used temporary Cloudflare Quick Tunnels tied to local processes.
- Assistant-spoken commitments as trusted evidence; the current contract intentionally validates caller evidence only.
