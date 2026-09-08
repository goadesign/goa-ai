# Workflow completion and saved-result upgrades

An agent workflow returns its final answer or successful terminal tool result.
It does not return another copy of every earlier tool result. For example,
reading ten individually valid data pages and producing a short report must
not make workflow completion fail because all ten pages were concatenated into
the returned value. The existing per-call workflow byte allowance is unchanged.

## Result contract

`api.RunOutput` (also available as `runtime.RunOutput`) contains:

- `Final` for a final assistant message, `FinalToolResult` for a tool-completed
  result, or `Suspension` when external input is needed;
- `ToolCount`, counting all tool results in the invocation, including failed
  attempts and results restored from earlier suspension checkpoints;
- `ToolTelemetry`, containing checked token and duration sums. Its model name
  is set only when all nonempty tool model names agree. Zero totals with no
  reported model names produce no summary. Conflicting nonempty model names
  preserve an empty summary even when totals are zero. Individual extra
  telemetry stays with its original stored event;
- the existing agent/run identity, planner notes, and model usage.

For a typed child result, the parent uses `FinalToolResult.Telemetry`, not the
combined summary. For a text child result, the parent uses `ToolTelemetry`.
Both use `ToolCount` for their child count. Historical failures do not turn an
eventual successful terminal result into a failure.

The runtime applies the same exact `contract.CopyRunOutput` encoding used by
the engines before storing success or suspension. An invalid or oversized
final output records failure with the codec's error. This does not make the
runtime Store update atomic with the workflow engine's later commit, nor does
it guarantee that every later engine operation succeeds.

## Reading complete diagnostics

Callers that previously inspected `RunOutput.ToolEvents` must change source:

- Use `FinalToolResult` for successful terminal-tool output and `Final` for
  assistant text. Do not load history to reconstruct a successful result.
- Use `Runtime.ListRunEvents(ctx, runID, cursor, limit)` when history is actually
  needed. Follow every returned cursor until the end. A page size batches reads;
  it is not a cap on the number of tools or records in an invocation.
- Decode tool-result records with `hooks.DecodeRunlogEvent`. The existing Store
  retains each full result, server data, failure message, telemetry, and call
  identity in recorded order. Concurrent results may be recorded in completion
  order; correlate them with their call identities instead of assuming request
  order. Continuation workflows have distinct run IDs and retain their own
  records; use the recorded predecessor relationships when inspecting a chain.

No new history store, history API, automatic full-history read, or model-visible
field is introduced by the result encoding. Suspension checkpoints use the
current `goa-ai.run-suspension.v8` contract and retain their existing tool history.
The separate recovery-catalog change requires the version upgrade described in
[runtime.md](runtime.md#coordinated-generated-system-releases).
The result encoding does not solve a potential
cumulative suspension-checkpoint size problem.

## Strict saved formats

New **top-level RunOutput** payloads use Temporal metadata
`encoding=json/goa-ai-run-output-v2`. Other values retain their existing
encodings, including nil payloads. Value and pointer result forms are supported.

A `json/plain` payload decoded into RunOutput selects one frozen private legacy
schema, then computes the same count and telemetry. The reader does not infer
the version from fields, retry another schema, accept unknown fields, rewrite
history, or silently omit malformed data. The new tag selects only the new
schema. Unknown tags, wrong-schema fields, malformed or trailing JSON,
oversized payloads, and invalid statistics remain errors. The byte allowance
continues to count data and encoding metadata together.

Applications that store RunOutput outside the workflow engine must also migrate
those saved copies under their own retention contract. Do not decode old JSON
directly into the new struct or remove unknown fields to make it fit. Use the
framework data converter with the saved encoding metadata; legacy unversioned
copies must be explicitly identified as the old `json/plain` schema.

## Deployment and rollback

Update source callers together with the runtime. Before activating new writers,
establish that no old active parent or client can receive a new-format result.
Worker pinning alone does not establish this: cross-deployment child routing
and ordinary retained-result reads can use a different caller's decoder.
No maintenance stop or history purge is required when this condition is proved.
Deploy a coordinated application release normally; this framework introduces
no readers-first flag, dual-write mode, or alternate runtime configuration.

If old active callers remain, their completion or an explicitly planned
temporary preparation release must be handled by the application before new
writers activate. Do not move a pinned workflow to new code without separate
replay-compatibility evidence. Old binaries cannot decode new-format results;
rollback must retain a compatible reader rather than blindly reverting the
decoder. Retained old results remain readable by the new reader immediately;
activation need not wait for their retention to expire.

## Removing the temporary legacy reader

The target architecture has only the new result schema. Delete the old reader
and migration-only tests/documentation after all supported deployments prove:

- No old-format writer can start or finish more work, including retained workers
  that can create child or continued executions.
- No open parent/workflow can replay an old child completion. Expiring only the
  child's own history is insufficient.
- Every closed history containing an old result, including parent histories,
  has actually ended its owner's retention and is no longer queryable or
  resettable through supported operations. Require deletion/absence evidence;
  deployment time plus a fixed number of days is not proof.
- Supported offline replay/exported histories and application-owned result
  copies have been migrated or have ended their supported retention.

These are saved-result conditions, not authority to remove saved suspensions or
canonical run-log records. The independent suspension-version release gate
still applies. Public consumers
must establish their own saved-copy and supported-retention obligations.
