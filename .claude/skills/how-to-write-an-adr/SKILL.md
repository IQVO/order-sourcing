---
name: how-to-write-an-adr
description: Write an Architecture Decision Record in this repo's numbering and format. Use when a design decision should be recorded, a new assumption is made where the model is silent, or a change contradicts an existing ADR.
---

# How to write an ADR

## Numbering and location

`docs/adr/NNNN-kebab-case-title.md`, four digits, sequential. Check the highest number with `ls docs/adr/`; never reuse
one. Add a row to `docs/adr/index.md` (`| [NNNN](./file.md) | title | Status (date) |`). The ADR index is the only place
the list lives. The existing ones are the examples: `docs/adr/0002-aggregates-invariants-and-assumptions.md` for a
decision with numbered assumptions, `docs/adr/0004-cloudevents-envelope-and-type-catalogue.md` for a catalogue.

## Format

```markdown
# ADR NNNN: title (a short noun phrase)

## Status

Accepted (YYYY-MM-DD).

## Context

The forces that make this decision necessary, with the alternatives seriously weighed (a reader later must see the
simpler option was considered).

## Decision

What was decided, in the present tense, numbered when there are several parts.

## Consequences

What becomes easier, what becomes harder, what to watch.
```

## Rules

- **Bodies are immutable.** A changed decision is a NEW ADR whose Status says it supersedes the old one; the old one's
  Status becomes `Superseded by ADR NNNN`. The one exception: ADR 0002's assumptions (A1, A2, ...) are numbered and
  referenced from code comments and tests, so a new assumption is appended as the next `A<n+1>`; never renumber.
- **Assumptions.** Where `model/model.yaml` (or the Model Gap Register) is silent, choose the least-surprising reading,
  record it as the next `A<n>` in ADR 0002 and report it as a model gap. Never edit the model or the docs repo from here.
- **The CloudEvents catalogue** (ADR 0004) is read by a fitness test: `type: ... stream: ...` lines must match
  `apis/asyncapi.yaml`; see `how-to-add-an-integration-event`.
- Cross-context decisions (an event shape, a call between contexts) are decided by the orchestrator in contracts-delta,
  not by one repo: cite it, do not contradict it.
- Name real files and Err variables; no company or brand names anywhere.

## After writing

`python3 scripts/harness/repo_lint.py`, `python3 scripts/harness/guide_lint.py` (every path an ADR or guide cites must
exist) and `make check`.
