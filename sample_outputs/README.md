# Sample Outputs

[← README](../README.md) · [Impact and evidence](../impact.md) · [Agent configuration](../agent/README.md)

All identities and businesses in this directory are explicitly synthetic. Secrets, raw credentials and private production data are excluded.

| File | What it demonstrates |
| --- | --- |
| [buyer.md](buyer.md) | Exact builder-generated buyer Context for `SYNTH-SELLER-001` |
| [seller.md](seller.md) | Exact builder-generated seller Context for the same GLID, isolated by role |
| [context-before.md](context-before.md) | Prepared Context while the synthetic NACH issue was still open |
| [context-after.md](context-after.md) | Rebuilt Context after a direct offline-resolution confirmation |
| [lookup-nach-resolution.json](lookup-nach-resolution.json) | Historical issue plus linked later resolution |
| [enrichment-accepted.json](enrichment-accepted.json) | Successful authenticated enrichment response |
| [enrichment-rejected.json](enrichment-rejected.json) | Durable retention with malformed extraction rejected |
| [real-call-verification.md](real-call-verification.md) | What actual Sarvam calls did and did not verify |
| [test-results.txt](test-results.txt) | Latest repeatable automated suite result |

The before/after NACH example used a direct manual confirmation processed through the normal authenticated Enrichment API. It was not represented as a fresh Sarvam voice extraction.

These artifacts distinguish automated fixtures, recorded real-call observations and manual synthetic confirmations. None contains a bearer token or private seller data.
