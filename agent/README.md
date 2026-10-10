# Sarvam agent configuration

[← Back to project README](../README.md)

| File | Purpose |
| --- | --- |
| [agent-details.md](agent-details.md) | Verified Agent ID, login-required dashboard link and pending published version |
| [greeting.md](greeting.md) | Opening greeting used by VANI |
| [instructions.md](instructions.md) | Memory-aware conversation and tool behavior |
| [extraction-prompt.txt](extraction-prompt.txt) | Exact shared `memory_contract_v1` output prompt |
| [hook-mappings.md](hook-mappings.md) | Start, Profile, Lookup and on-end endpoint mappings |

The bearer token belongs only in Sarvam's secure authentication field. The `enrichment_json` output variable must use type **String** and contain valid JSON without Markdown fences.

Before submission, add the final `app_version` from the Sarvam dashboard or export and confirm that version uses the packaged prompt and mappings. The supplied dashboard URL requires login and is not a public live/demo link.
