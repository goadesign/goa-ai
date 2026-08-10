---
name: agentic-tool-design
description: Use when designing or reviewing the tools an LLM agent will call — choosing tool boundaries, argument and result shapes, retrieval/search/get decompositions, MCP or goa-ai toolset surfaces — or when an agent misuses existing tools (wrong tool picked, IDs carried between calls, wrong dates computed, confident answers about absent data).
---

# Agentic Tool Design

## Overview

Tools are contracts between a deterministic system and a non-deterministic
caller. The model's only comparative advantage is language: understanding
the question and phrasing the answer. Everything between — retrieval,
filtering, date resolution, joining, counting, correlation — is mechanical,
and every mechanical step left on the model's side of the contract is a
place it can silently be wrong.

**Design tools question-shaped, not entity-shaped.** An entity-shaped
surface mirrors storage: list handles, dereference handles (`list` →
`get(id)`). A question-shaped surface mirrors the caller's job: one
self-contained request in, complete answer-ready evidence out.

## The recipe

Design the toolset in this order:

1. **Enumerate the caller's real questions**, not the store's entities.
   Work backwards from twenty concrete questions users will actually ask.
2. **One tool per capability seam.** A seam is a different corpus, a
   different side effect, or a different trust level — *never* a different
   field projection of the same retrieval. A cost tool, a when tool, and a
   bring tool are column masks over one query; they belong as fields of one
   result.
3. **One call per referent; all projections travel together.** If two
   calls each run retrieval for "the camp trip", nothing guarantees they
   resolve to the same record — the answer fuses camp A's cost with camp
   B's date. Return the full dossier (logistics, money, lists, source
   attribution) in the retrieval result. Token thrift via summary-then-
   detail splits is almost always premature: measure first; twenty full
   dossiers is typically a few thousand tokens.
4. **Pre-compute every mechanical fact in-band.** Named date windows
   (`this_week`, `friday`) resolved server-side in the right timezone and
   echoed back resolved; true `total` under any row cap; sums when numeric
   fields appear. The model reports arithmetic; it never performs it.
5. **State the corpus's own limits in the envelope.** An honest absence
   claim ("nothing this weekend") requires the tool to say what was
   coverable: `coverage: since <date>, <n> sources`. Without it the model
   will confidently overclaim what the corpus never contained.
6. **Semantic values in, semantic values out.** Kid/user/project *names*
   as the model speaks them, resolved against the closed roster server-side;
   ambiguity returns a correction listing the real candidates. No UUIDs in
   results — when the exit needs grounding, number the evidence (`#1, #2`)
   and let the server map ordinals back to IDs from the run journal.
7. **A validated exit tool.** The answer is a tool call whose factual
   claims must cite evidence ordinals, validated against what the run's
   tools actually returned; a negative answer is grounded by the journal's
   record of what was checked, which the model cannot author.
8. **Errors teach.** Every rejectable call returns a correction the model
   can act on ("'vienna' names a tracked kid — pass kid: 'Vienna'"), never
   a bare failure.

## The placement principle

**Put model intelligence where its mistakes are loud.** Wrong arguments
fail loudly and corrections teach mid-run. Wrong *tool selection* among
overlapping tools succeeds silently — the when-tool returns *a* date and
nobody errors. So: few tools with rich arguments over many tools with thin
ones, and overlap between tool descriptions is a defect, not a convenience.

## Boundary test

If the model must remember anything between two tool calls — an ID, a
filter it already passed, which record it meant — the boundary is drawn
wrong. Each call self-contained; each result answer-ready.

## Quick reference

| Symptom in a design | Defect | Fix |
|---|---|---|
| `get_x(id)` after `list_x`/`search_x` | ID plumbing | Retrieval returns full dossiers |
| Two retrieval tools accepting the same filters | Overlap → silent mispick | One tool per corpus; split only on capability seams |
| Model computes dates, counts, or sums | Mechanical work on the model | Named windows, `total`, `sums` in the envelope |
| Empty result ⇒ model says "there is none" | Unbounded absence claim | `coverage` bounds + journal-grounded negatives |
| UUIDs in results or citations | Correlation on the model | Ordinal references; server owns the mapping |
| "cost_of", "when_is", "who_sent" tools | Projections dressed as tools | Fields of one dossier |
| Retrieval matches titles only | Recall gap for prose questions | Match bodies too; return the matched quote as evidence |

## When many tools ARE right

Splitting is correct across genuine capability seams: read vs. mutate
(side effects are the strongest seam), different corpora (structured
records vs. raw source text), different cost/latency tiers, different
permission levels. Selection between *disjoint* capabilities is a decision
models make well; selection between overlapping projections is not.

## Evals arbitrate

Every rule above is testable: score answer-correctness per question class
against the toolset. If a design choice can't move that score, it is
taste, not design. Keep a held-out set; tool descriptions are prompt
surface and overfit like any prompt.
