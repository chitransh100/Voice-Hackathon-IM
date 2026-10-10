# Approach

[← README](README.md) · [Impact and evidence](impact.md) · [Build journey](skills.md)

## Problem 2: shared memory across channels

A seller may discuss an issue on one call, share information through another channel and continue later. Without shared memory, the next conversation starts from zero: details are repeated, preferences are forgotten and unrelated issues can be mixed together.

VANI gives each `(glusr_id, role)` pair one structured memory. Buyer and seller records remain separate even when they use the same GLUSR ID.

## Core approach

VANI separates everyday context from detailed history:

1. **Prepared Context** provides a short briefing before every conversation.
2. **Profile** supplies the complete nested business profile.
3. **Lookup** retrieves older evidence only when the conversation needs it.
4. **Enrichment** validates and saves supported post-call information, then rebuilds Context.

All four services use the same SQLite database and the same three tables: `users`, `interactions` and `current_context`.

## What stays in Context

The default recent-conversation window is **15 UTC days**. Context also keeps important durable information even when its source is older:

- current preferences;
- restrictions;
- unresolved issue threads; and
- commitments.

Older interactions remain stored and searchable through Lookup.

Each issue has its own thread. Resolving a login problem therefore does not close an unrelated NACH or catalogue problem.

## Why Context and Lookup are separate

Sending all history into every conversation would make the prompt longer and hide current priorities. VANI instead uses:

- a compact prepared briefing for the common path; and
- a bounded, source-linked search for occasional older detail.

The complete profile is returned separately, so it is not silently shortened to fit the Markdown budget.

## Keeping memory trustworthy

Sarvam proposes the summary, structured points and profile updates. The backend does not run an LLM; it applies deterministic checks before information enters trusted memory.

- Evidence must exactly match a caller transcript variant.
- Invalid points stay outside trusted Context.
- Valid independent points can still be accepted.
- Duplicate deliveries do not create duplicate interactions.
- Changed deliveries use explicit revisions.
- Failed Context rebuilds remain retryable.

The raw authenticated conversation is retained when nested extraction is malformed, allowing a corrected revision without treating rejected content as fact.

## Evidence boundary

Automated tests verify the memory behavior and synthetic fixtures demonstrate cross-channel data handling. Real Sarvam calls verify the voice start/end API path. A live WhatsApp or web-chat writer followed by a voice continuation was not demonstrated.

See [impact.md](impact.md) for measurements, jury-criteria mapping and material limitations.
