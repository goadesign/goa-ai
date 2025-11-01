# Tool Validation and Retry Hints — Action Plan

This document turns the recent critique into an actionable plan to improve how we map tool validation errors into structured ToolError and RetryHint objects, maximizing usefulness to LLMs while keeping runtime behavior robust.

## Objectives

- Validate tool inputs at the boundary; do not block or hide tool results.
- Replace brittle string parsing with structured, schema‑aware details.
- Produce concise, actionable hints (missing fields, constraints, clarifying question) that LLMs can use immediately.

## Scope (What will change)

- Codegen for tool specs (validations + codecs)
- Runtime hint builder (schema‑aware)
- Registration‑time schema caching
- Tests (unit + generated code goldens)

## Actionable Items

### 1) Structured Validation Output (Codegen)

- Generate validators for payload types that produce structured field issues in addition to returning an `error`:
  - Define a local `ValidationIssue` shape: `{ Field string, Constraint string, Expected any, Got any }`.
  - Extend generated `ValidateX` to build a slice of `ValidationIssue` alongside the merged Goa errors.
  - Wrap both in a small local error type implementing:
    - `error`
    - `Issues() []ValidationIssue`
  - Maintain existing Goa error semantics for compatibility; the structured data augments it.
- Do NOT generate result validations (payload‑only policy remains).

### 2) Runtime Hint Builder (Prefer Structured → Fallback)

- Add a runtime helper that converts validation errors to RetryHint with this order:
  1. If the error implements `Issues() []ValidationIssue`, use it directly.
  2. Otherwise, fallback to the current regex/path extraction + schema fallback.
- Map issues to RetryHint fields:
  - `Reason`: `invalid_arguments` for payload; `malformed_response` for result.
  - `MissingFields`: derived from required or issues with `Constraint == "required"`.
  - Add fields for constraints when applicable (limit size):
    - `AllowedValues` (enum), `Pattern`, `Min`, `Max`, `Format`.
  - `ClarifyingQuestion`: one concise question using field descriptions; cap to top 1–3 fields.
- Keep ToolError message short (first pertinent error); avoid boilerplate.

### 3) Schema Caching and Helpers

- Cache parsed tool JSON Schemas at registration time (`rt.RegisterToolset`):
  - Build property name maps (canonical ↔ schema key).
  - Pre‑extract: `required`, enums, format, descriptions.
- Expose internal helpers:
  - `requiredPropertyKeys(tool) []string`
  - `propertyDescription(tool, key) string`
  - `allowedEnumValues(tool, key) []string`
  - `formatFor(tool, key) string`
  - `canonicalKey(s string) string` and mappers (nested paths handled with dotted notation).

### 4) Nested/Union Support

- Normalize nested field paths as `parent.child` (dotted) in issues and hints.
- For union/oneOf inputs, detect and hint on discriminator/choice requirements.

### 5) LLM‑Oriented Guardrails

- Cap hint verbosity:
  - Max 3 fields in `MissingFields` / ClarifyingQuestion.
  - Max 5 enum values in `AllowedValues` (append `…` when truncated).
- Prefer schema descriptions over raw field names when building questions.
- Provide `ExampleInput` with placeholders only for missing fields (optional, small and focused).

### 6) Activities Mapping (Boundary Behavior)

- Inputs (Args): on decode/validation failure
  - Return `ToolOutput{Error, RetryHint}` with `invalid_arguments` and enriched details.
  - Do not abort workflow; planners can auto‑repair.
- Results: on encode/validation failure
  - Return `ToolOutput{Error, RetryHint, Payload: best-effort JSON}` with `malformed_response`.
  - Do not block; do not request user action.

### 7) Tests

- Codegen unit tests:
  - Validators produce `Issues() []ValidationIssue` with correct field paths for nested objects.
  - Goldens updated to show payload‑only validation calls.
- Runtime unit tests:
  - Hint builder prefers `Issues()`; regex fallback only when needed.
  - ClarifyingQuestion respects descriptions and caps length.
- Integration checks (example + Aura):
  - Regenerate, build, and run basic tests to ensure no undefined `Validate*` symbols; no regressions.

### 8) Migration & Backward Compatibility

- Keep ToolError shape unchanged (message + cause); all structure goes into RetryHint.
- Roll out structured `Issues()` behind a minor version bump in codegen; keep string-only fallback operational.
- No transport changes; runtime boundary behavior remains stable.

### 9) Acceptance Criteria

- Inputs: Invalid payload → RetryHint with specific missing/invalid guidance and a single, helpful clarifying question.
- Results: Invalid result → best‑effort payload forwarded; hint marks provider error without asking for user input.
- No codegen compile errors in downstream repos (e.g., Aura) after regeneration.
- Unit tests cover structured issues and schema‑aware hinting.

## Sequencing

1. Implement structured `Issues()` in payload validators (codegen + goldens).
2. Add runtime hint builder that consumes `Issues()` + fallback to schema parsing.
3. Cache schemas at registration; update helpers.
4. Update activities (boundary mapping) – already aligned to inputs‑only validation.
5. Update tests and regenerate example/Aura; fix any compile issues.

## Notes

- Keep result validation generation off to avoid hiding potentially useful output from LLMs.
- When structured issues are not available (older builds), fallback logic preserves current behavior.

