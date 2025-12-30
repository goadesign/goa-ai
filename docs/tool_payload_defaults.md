# Tool Payload Defaults (Goa‑Style)

Goa‑AI generates **typed tool payload structs**, **JSON schemas**, and **codecs** from your Goa
design. This document explains a critical behavior: **how default values are applied** for tool
payloads and why this is coupled to pointer vs value field shapes.

This is implemented to match Goa’s own HTTP “decode‑body → transform” model.

## Summary

- **Decode JSON into a helper type** with pointer fields (“decode‑body” shape) so the codec can
  distinguish **missing** from **zero**.
- **Transform helper → final payload** using Goa’s `codegen.GoTransform`.
- For **tool payloads**, we set `useDefault=true` on the *target* payload type so that:
  - optional primitive fields with defaults become **values** (non‑pointers), and
  - `GoTransform` can **inject defaults** deterministically when helper fields are missing.

If these contexts do not match, the generator can emit invalid nil checks or invalid assignments and
the generated code will not compile.

## What “useDefault” means

Goa’s pointer/value decision for primitive fields depends on a `useDefault` flag:

- When `useDefault=true`, an **optional** primitive (or aliased primitive) field that has a default
  is emitted as a **value** field (not a pointer). This allows “defaulting” to happen by direct
  assignment into a value field.
- When `useDefault=false`, optional primitives remain pointers more often, preserving “presence”
  semantics.

In goa‑ai, we treat **tool payloads as request‑like**, so payload struct generation uses
`useDefault=true`.

## Generated shapes

### 1) JSON decode‑body helper type (pointer fields)

The codec first decodes incoming JSON into a helper struct whose fields are pointers for
primitives:

- missing field → `nil`
- provided field → non‑nil pointer

This is the shape used for:

- required field checks
- validation error attribution
- “did the caller provide this field?”

### 2) Final tool payload type (default‑aware, value fields)

The final tool payload type is what adapters and executors consume.

For payloads, defaulted optional primitives are emitted as values so defaults can be applied
deterministically during transformation.

## How defaults are applied

Defaults are applied during **helper → payload transformation**:

- The helper contains `nil` pointers for missing fields.
- The target payload is a value‑field struct for defaulted optionals.
- Goa’s `codegen.GoTransform` emits code that:
  - copies values when helper pointers are non‑nil
  - assigns default literals to target fields when helper pointers are nil (and a default exists)

This matches Goa’s transport generators: validation happens on the decode‑body helper, then
transformation produces the final type with defaults applied.

## Generator maintainer contract (do not break this)

When changing codegen that touches any of the following:

- tool payload type materialization
- decode‑body helper generation
- adapter transforms (tool payload → service method payload)

you must keep `UseDefault` consistent:

- **Tool payload type generation** must use `useDefault=true`.
- **Transforms that read tool payload fields** must use an AttributeContext with `UseDefault=true`
  for the tool payload side.

If you mismatch them, Goa’s transform generator will treat value fields as pointers (or vice versa)
and generate uncompilable code such as:

- `if in.Field != nil { ... }` when `Field` is a value
- `out.Field = "x"` when `Field` is a `*T`

## Where this is implemented

- Tool payload type materialization (useDefault for payloads):
  - `codegen/agent/specs_builder_type_info.go`
  - `codegen/agent/specs_builder_materialize.go`
- Toolset adapter transforms (tool payload → service method payload):
  - `codegen/agent/generate_toolset_transforms.go`


