# Sample outputs

[← Back to project README](../README.md)

All identities and businesses in this directory are explicitly synthetic. Secrets, raw credentials and private production data are excluded.

| File | What it demonstrates |
| --- | --- |
| `buyer.md` | Exact builder-generated buyer Context for `SYNTH-SELLER-001` |
| `seller.md` | Exact builder-generated seller Context for the same GLID, isolated by role |
| `context-before.md` | Prepared Context while the synthetic NACH issue was still open |
| `context-after.md` | Rebuilt Context after a direct offline-resolution confirmation |
| `lookup-nach-resolution.json` | Historical issue plus linked later resolution |
| `enrichment-accepted.json` | Successful authenticated enrichment response |
| `enrichment-rejected.json` | Durable retention with malformed extraction rejected |
| `real-call-verification.md` | What actual Sarvam calls did and did not verify |
| `test-results.txt` | Latest repeatable automated suite result |

The before/after NACH example used a direct manual confirmation processed through the normal authenticated Enrichment API. It was not represented as a fresh Sarvam voice extraction.

These artifacts distinguish automated fixtures, recorded real-call observations and manual synthetic confirmations. None contains a bearer token or private seller data.
