# Inject Generalization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox syntax.

**Goal:** Make `Inject` deliver its full promise: injected tool-payload fields are populated for BOTH topologies (registry-served providers and in-process executors) from generation-time-resolved sources (ToolCallMeta fields or run labels), with invalid combinations rejected at codegen and one generated implementation shared by both paths.

**Architecture (agreed with the maintainer, codegen-maximal):** injection is compiled, not interpreted. Codegen resolves each injected name to its source at generation time — names matching the fixed ToolCallMeta set (sessionId→SessionID, runId→RunID, turnId→TurnID, toolCallId→ToolCallID, parentToolCallId→ParentToolCallID) compile to direct meta reads; every other name compiles to a typed label binding with the field's existing generated validation applied and a compiled missing-label error. Per-toolset generated inject functions live beside the codecs; the generated local-executor registration wraps user executors as decode → inject → execute; the generated registry provider.go calls the SAME functions (asymmetry dies). Toolset registrations carry a generated required-label-keys list; the runtime validates label presence at RUN START (union over the agent's used toolsets) so violations fail before any model call. Runtime's only contributions: carrying labels (already exists) and the two integration points.

**Non-negotiable constraints:** existing `Inject("sessionId")` designs (the reference consumers) regen to identical behavior with zero design edits — non-breaking. Partial-evaluation rule: any branch knowable at generation time is emitted, never evaluated at runtime — no reflection, no spec-walking, no generic map plumbing beyond the labels input. AGENTS.md binds throughout (declaration order, one canonical implementation, fail fast, docs-in-sync).

## Global facts (verified this session)

- Population today exists ONLY in generated registry provider.go (`methodIn.SessionID = msg.Meta.SessionID`) and only for meta-backed names; local executors get nothing; DSL accepts any name silently.
- Labels already survive the Temporal workflow→ExecuteToolActivity boundary verbatim (proven downstream).
- codegen/agent: prepare.go flattenAndHide handles InjectedFields (schema hiding — KEEP); data.go/data_toolsets.go carry InjectedFields; generate_toolset_specs.go emits specs/codecs/types(+http validate)/provider.

### Task 1: Codegen — compiled injection glue + generation-time validation

`codegen/agent/`: (1) source resolution: meta-name table (the five, lowerCamel design-name → ToolCallMeta field) vs label-backed (everything else); (2) emit per-toolset `inject.go` beside codecs: one exported typed function per tool (`InjectEmitEvent(p *EmitEventPayload, meta runtime.ToolCallMeta, labels map[string]string) error` — exact naming per existing gen conventions; direct assignments; label conversions through the field's named type + existing generated validation; compiled missing-label/failed-validation errors with precise text); (3) toolset registration data gains `RequiredLabels []string` (generated constant list + GoDoc naming the WithLabels obligation); (4) generation-time errors: injected name absent from payload (verify existing behavior — tighten if soft), injected name colliding with a required-from-model field, empty Inject; (5) regenerate ALL in-repo test scenarios/examples; codegen golden/unit tests for: meta-backed, label-backed w/ typed validation, mixed, generation errors. DISCOVERY FIRST: read generate_toolset_specs.go + provider generation end to end; the http/validate transport types path (injected fields must stay excluded from model schema + wire codec — regression-test that hiding is untouched).

### Task 2: Runtime — both topologies consume the generated glue

(1) Local path: the generated `WithXExecutor` registration option (or the runtime's tool-dispatch seam if the wrapper belongs there — DISCOVER which layer owns decode today; injection goes exactly where decode already happens, one seam) calls the generated inject fn between decode and Execute; ToolCallMeta already reaches executors — verify labels do too on the local path (they ride ToolRequest.Labels; confirm ExecuteToolActivity hands them through on BOTH engines — inmem + temporal). (2) Registry provider.go generation: replace its inline meta assignments with calls to the same generated inject fns (behavior-identical for sessionId — regression test). (3) Run-start validation: Runtime.Start/OneShotRun (DISCOVER exact entry points) computes the union of used toolsets' RequiredLabels from registration data and fails fast with an error naming the missing keys BEFORE any workflow/model activity. (4) Tests: inmem-engine end-to-end (label set at start → typed field populated in executor; missing label → run-start error; malformed label value → precise tool-call error), temporal-engine test if the repo has the harness (DISCOVER; else document), provider-path regression (sessionId identical), cross-engine label-carry already-proven cite.

### Task 3: Docs, examples, final review, PR

dsl/tool.go Inject GoDoc rewritten (sources, both topologies, run-start contract, generation-time guarantees); docs/runtime.md injection section; dsl/doc.go line updated; example designs exercising a label-backed injection; CHANGELOG-equivalent per repo convention if any. Whole-branch final review (most capable model; findings fixed), then push branch `inject-generalization` + `gh pr create` (title "feat(codegen,runtime): compile Inject population for both topologies and label-backed fields"; body = design summary from this plan + the downstream motivation). NO merge/tag/release — owner decides.

## Self-review notes

- Non-breaking proof obligation sits in T1/T2 tests (sessionId regen-identical); the reference consumers' upgrade path is regen-only.
- The one genuinely open seam (where decode happens for local executors → where inject slots in) is a T2 discovery with a stop-and-escalate if the layering fights the one-canonical-implementation goal.
- Downstream follow-up (separate, post-release): osita drops its hand-rolled householdId label reads for the typed injected field.
