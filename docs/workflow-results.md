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
- the required generated `Subject`, run identity, planner notes, and model usage.
  `Subject` selects one actual Agent executor or one service tool and its
  configured Registry source. See [Run subjects](run-subjects.md).

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
field is introduced by the result encoding. This change did not change the
suspension schema. Current continuation restoration requires
`goa-ai.run-suspension.v14`, which references saved history while retaining active tool and recovery state. The separate
preparation and checkpoint upgrade requirements are described in
[Runtime Store](runtime.md#runtime-store-storagestore).
The result encoding itself does not change suspension-checkpoint size limits.

## Strict saved formats

New **top-level RunOutput** payloads use Temporal metadata
`encoding=json/goa-ai-run-output-v3`. Other values retain their existing
encodings, including nil payloads. Value and pointer result forms are supported.

Version three requires the generated `Subject` in place of `AgentID`. The
strict reader validates the original subject JSON before decoding a union, so
unknown fields, duplicate subject fields, and combined branches cannot disappear
during decoding. Missing subjects, old `AgentID` fields, malformed or trailing
JSON, oversized transport payloads, and invalid statistics are errors.
The transport byte allowance still counts data and encoding metadata together.

The current reader rejects earlier version-two and unversioned RunOutput
payloads. It does not infer a format from fields, recover a missing subject from
an AgentID, or rewrite a saved result. Applications that store copies outside
the workflow engine must convert those copies explicitly under their owning
storage contract.

## Deployment and rollback

This is a breaking source and saved-data change. Update all common run envelopes,
Store implementations, record consumers, and generated application transports
together. Follow the complete [run-subject upgrade requirements](run-subjects.md#upgrade-required).

Before activating new writers, account for open workflows, saved starts and
preparations, suspension checkpoints, child results embedded in parent history,
closed queryable histories, exported replay files, and application-owned result
copies. Finishing an old child does not remove its result from a parent's history.
Retain old code for work that still requires its format, or complete an explicit
owner-controlled conversion with replay evidence before switching that work.
Do not move a pinned workflow onto the new decoder merely because it is idle.

Old binaries cannot decode version-three results; new binaries cannot read the
removed result formats. A rollback must restore matching data and request bytes
or retain the matching execution owner. Neither a deployment date nor a guessed
retention interval proves that old data is gone. There is no compatibility flag,
dual write, or automatic legacy reader in the target runtime.
