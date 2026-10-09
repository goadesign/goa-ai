---
name: agentic-tool-design
description: Use when designing or reviewing the tools an LLM agent will call — choosing tool boundaries, argument and result shapes, retrieval/search/get decompositions, MCP or goa-ai toolset surfaces — or when an agent misuses existing tools (wrong tool picked, IDs carried between calls, wrong dates computed, confident answers about absent data).
---

# Agentic Tool Design

## Overview

Tools are contracts between a deterministic system and a non-deterministic
caller. The model interprets user intent, relevance, ambiguity, and tradeoffs.
Code owns facts derivable from authenticated context, typed contracts, and
execution state: identity, authorization, correlation, pagination, exact
calculations, and valid transitions. Keep semantic choices model-driven while
enforcing their legal shape through the design.

**Design tools around the caller's goal.** Combine retrieval steps that only
move implementation-owned values between calls. Keep separate operations when
the model genuinely selects a resource or chooses a different capability. A
search followed by `get(id)` is valid when the search enables that choice.

## The recipe

Design the toolset in this order:

1. **Enumerate the caller's real questions**, not the store's entities.
   Work backwards from representative questions users will actually ask.
2. **One tool per capability seam.** A seam is a different corpus, a
   different side effect, or a different trust level — *never* a different
   field projection of the same retrieval. A cost tool, a when tool, and a
   bring tool are column masks over one query; they belong as fields of one
   result.
3. **One call per referent; all projections travel together.** If two
   calls independently resolve the same project name, they may select
   different projects. Return related evidence together when the caller needs
   it. Measure result size and use Goa-AI's bounds and pagination contracts
   when complete evidence needs more than one page.
4. **Pre-compute every mechanical fact in-band.** Named date windows
   (`this_week`, `friday`) resolved server-side in the right timezone and
   echoed back resolved; true `total` under any row cap; sums when numeric
   fields appear. The model reports arithmetic; it never performs it.
5. **State the corpus's own limits in the envelope.** An honest absence
   claim ("nothing this weekend") requires the tool to say what was
   coverable: `coverage: since <date>, <n> sources`. Without it the model
   will confidently overclaim what the corpus never contained.
6. **Semantic values in, semantic values out.** User or project *names*
   as the model speaks them, resolved against the closed roster server-side;
   ambiguity returns a correction listing real candidates. Stable resource
   identifiers stay visible when the model selects among those candidates.
   Keep cursors, continuation state, credentials and execution correlation
   outside model arguments; reuse generated metadata and runtime ownership.
7. **Validate required answer evidence.** When an application requires
   citations or a typed final answer, define that contract and check it against
   accepted evidence. Do not assume the framework supplies an evidence-ordinal
   journal or requires every agent to finish through an exit tool.
8. **Errors teach.** Every rejectable call returns a correction the model
   can act on, such as the missing field and a valid authored example. Keep
   authorization failures and unknown tool outcomes distinct from correctable
   model arguments.

## The placement principle

**Put model intelligence where its mistakes are loud.** Wrong arguments
fail loudly and corrections teach mid-run. Wrong *tool selection* among
overlapping tools succeeds silently — the when-tool returns *a* date and
nobody errors. So: few tools with rich arguments over many tools with thin
ones, and overlap between tool descriptions is a defect, not a convenience.

## Boundary test

Trace each value between calls. A stable resource ID used for a real selection
is different from an opaque cursor or continuation copied without a decision.
Let runtime code own the latter, with explicit ordering and parallel-call
semantics. Review valid caller choices beyond the failing example.

## Quick reference

| Symptom in a design | Defect | Fix |
|---|---|---|
| A follow-up call only copies an opaque transport value | Completion assigned to the model | Bind it through generated metadata or runtime state |
| Two retrieval tools accepting the same filters | Overlap → silent mispick | One tool per corpus; split only on capability seams |
| Model computes dates, counts, or sums | Mechanical work on the model | Named windows, `total`, `sums` in the envelope |
| Empty result ⇒ model says "there is none" | Unbounded absence claim | Declared coverage and accepted evidence |
| Model arguments repeat a cursor, continuation, or execution identity | Mechanical correlation assigned to the model | Runtime owns it; retain stable IDs for genuine resource choices |
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
