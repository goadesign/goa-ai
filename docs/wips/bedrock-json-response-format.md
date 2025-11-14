# Bedrock JSON-Only Responses (ResponseFormat) for Tools and Non‑Tool Turns

## Desired Outcome

Enable agents to request strict JSON responses from the model, with optional schema validation, across both tool and non‑tool turns:

- When an agent (or agent‑as‑tool) needs a machine‑readable reply, the model should return only JSON — no prose or markdown — enforced by the provider (`response_format`) rather than by prompt text.
- Prefer schema‑constrained JSON (`json_schema`) using the same result schemas we expose to the model for tools. Fall back to JSON‑only (`json_object`) when a schema is unavailable.
- Apply equally to tool and non‑tool scenarios. For ADA and other agent‑as‑tool cases, this integrates naturally with JSONOnly semantics.
- Keep elegant, provider‑aligned contracts:
  - One knob on `model.Request` to express desired format
  - Bedrock encodes `response_format`; OpenAI path uses a strict fallback + post‑validation
  - Disable thinking where JSON‑only is requested

## Plan (Reader‑Friendly)

1) Add ResponseFormat to goa‑ai model
- Introduce `model.ResponseFormat`:
  - `JSONSchema any` (optional)
  - `SchemaName string` (optional but recommended)
  - `JSONOnly bool` (true means “JSON only”; used when schema is absent)
- Add `ResponseFormat *ResponseFormat` to `model.Request`.

2) Bedrock adapter encodes response_format
- If `ResponseFormat.JSONSchema != nil`:
  - Set `AdditionalModelRequestFields.response_format = { type: "json_schema", json_schema: { name: SchemaName, schema: JSONSchema } }`.
- Else if `ResponseFormat.JSONOnly`:
  - `response_format = { type: "json_object" }`.
- Disable thinking for JSON‑only/schema‑constrained turns.

3) OpenAI adapter fallback
- When `ResponseFormat` is set:
  - Inject a strict “return only JSON” instruction; ignore schema enforcement (provider does not support).
  - Post‑validate JSON and return a precise RetryHint if invalid.

4) AURA inference‑engine Request wiring
- Extend `services/inference-engine` design:
  - Add `ResponseFormat` with fields: `type: enum("json_object","json_schema")`, `schema:any`, `name:string`.
  - Add optional `response_format` to `Request`.
- Implementation maps `Request.ResponseFormat` → `model.Request.ResponseFormat`.

5) Planner usage (non‑tool turns)
- Planners can set `ResponseFormat` ad‑hoc (e.g., “emit a JSON object with X/Y”). Unified across providers.

6) ADA / agent‑as‑tool usage
- Keep JSONOnly semantics for agent‑as‑tool. Long‑term (codegen path) we will:
  - Detect tool result schema from generated specs and supply it to `ResponseFormat`.
  - Set JSON‑only + disable thinking for these turns.
- Short‑term: ADA planners can opt‑in by setting `ResponseFormat` directly (non‑streaming).

7) Validation & RetryHints
- After model completion, validate JSON if a schema was specified.
- Build a `RetryHint` with missing/invalid fields for the planner/tool path.

8) Streaming
- Streaming remains tool‑call oriented; JSON‑only is a unary path (no streaming) to keep contracts clear.

9) Tests & Docs
- Add/adjust tests around Bedrock encoding and inference‑engine wiring.
- Document planner usage patterns; discourage prose prompts for JSON‑only behavior.

## Progress Tracker

- [ ] 1. Add `model.ResponseFormat` and `Request.ResponseFormat` in goa‑ai
- [ ] 2. Bedrock: encode `response_format`, disable thinking under JSON‑only/schema
- [ ] 3. OpenAI: strict JSON fallback (instruction) and post‑validation
- [ ] 4. AURA design: add `response_format` to inference Request
- [ ] 5. AURA impl: map Request.ResponseFormat → model.Request.ResponseFormat
- [ ] 6. Optional: ADA planner opt‑in example (non‑streaming)
- [ ] 7. Tests: provider encoding + inference‑engine mapping
- [ ] 8. Lint both repositories
- [ ] 9. Update docs references where needed
- [ ] 10. Review for elegance and consistency


