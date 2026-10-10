# Sarvam tool and hook mappings

All protected calls use Sarvam's secure **Bearer token** field. Use the same backend token for every service. Do not expose it as an agent variable.

Replace each `<HTTPS_BASE_...>` placeholder with the stable deployed service origin. Temporary Quick Tunnel addresses used in the recorded demo are deliberately not packaged as permanent endpoints.

## 1. Start context

```text
Method: GET
URL: <HTTPS_BASE_CONTEXT>/v1/context
Lifecycle: conversation start / pre-conversation
```

Query mappings:

| Query field | Sarvam source |
| --- | --- |
| `glusr_id` | trusted backend-bound seller/buyer GLUSR variable |
| `role` | trusted backend-bound `seller` or `buyer` variable |

Useful response mappings:

| Response field | Agent variable |
| --- | --- |
| `context_md` | `context_md` (String, visible to the agent) |
| `business_name` | `business_name` |
| `city` | `city` |
| `profile_json` | structured profile variable if supported |
| `profile_updated_at` | `profile_updated_at` |

## 2. Complete profile read

Use when the workflow loads the profile separately from prepared Context:

```text
Method: GET
URL: <HTTPS_BASE_PROFILE>/users/{glusr_id}/profile?role={role}
Lifecycle: pre-conversation
```

Map `glusr_id` and `role` only from trusted workflow variables. Do not let the LLM choose them.

## 3. `lookup_history`

```text
Method: POST
URL: <HTTPS_BASE_LOOKUP>/v1/history/lookup
Content-Type: application/json
Lifecycle: callable during conversation
```

Request body:

```json
{
  "glusr_id": "<trusted GLUSR variable>",
  "role": "<trusted role variable>",
  "topic_keyword": "<short dynamic tool argument>",
  "lookback_days": "90"
}
```

Tool argument rules:

- `topic_keyword`: required short English phrase; never blank.
- `lookback_days`: required integer or integer text from 1 to 3650; use fixed text `90` for the demo.

Response mappings:

| Response field | Type | Use |
| --- | --- | --- |
| `found` | Boolean | Whether matching retained evidence exists |
| `context` | String | Dated evidence, sources and linked updates, or a clear no-match message |

## 4. Enrich conversation on end

```text
Method: POST
URL: <HTTPS_BASE_ENRICHMENT>/interactions/enrich
Content-Type: application/json
Lifecycle: on end
```

Exact mappings:

| Request field | Type | Sarvam source |
| --- | --- | --- |
| `glusr_id` | String | trusted backend-bound GLUSR variable |
| `role` | String | trusted `buyer`/`seller` variable |
| `source` | Text | literal `sarvam` |
| `source_event_id` | String | Interaction ID |
| `channel` | Text | literal `voice`, or another trusted channel |
| `start_datetime` | String | Call start time |
| `session_state` | Text | literal `completed` |
| `transcript` | String or transcript-turn array | Call Transcript |
| `enrichment_json` | String | output variable `enrichment_json` |

Omit top-level `thread_id` unless the workflow has one trusted call-level thread. Per-point thread IDs belong inside `important_points_json`.

Do not map Sarvam's timezone-less Call start time to `occurred_at`. Send it as `start_datetime`; the API interprets it only using the explicitly configured `VANI_TIMEZONE`. A trusted source may use `occurred_at` instead only when it already supplies timezone-aware RFC3339.

`enrichment_json` is a JSON-encoded String, and its `important_points_json` field is another JSON-encoded String. Configure the output variable type as **String** and use `extraction-prompt.txt` verbatim.
