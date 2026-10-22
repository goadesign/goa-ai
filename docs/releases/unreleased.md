# Unreleased — Preserve continuation workflow metadata

The runtime package submits workflows that resume direct or registered tool
operations after a host supplies an answer. These successor workflows now retain
the caller's Memo, the metadata attached to a workflow in the engine. Previously,
the Runtime submission and completion methods could not accept that metadata.

Each successor supplies its own Memo; the runtime does not inherit it from the
predecessor. The runtime saves the encoded values before submitting the workflow.
After a lost reply or worker restart, it reuses the exact saved request. Changing
the answer or Memo for the same successor returns `storage.ErrSeedConflict`
before another engine submission. Already published requests remain immutable.

## Required action

Add a final `memo map[string]any` argument to Runtime `StartToolContinuation` and
`EnsureToolContinuationCompletion` calls. Supply the same values for submission
and completion. Pass `nil` for empty Memo, including when recovering requests
that were published without metadata. A different successor may supply different
metadata.

This is a Go source API change. It does not change the stored recipe format or
registry wire protocol, and it does not require generated code or data migration.
Client `PrepareContinuation` and `PrepareOneShotContinuation` continue to accept
per-workflow Memo through `WorkflowOptions`.

See [durable tool continuations](../runtime.md#external-input-and-workflow-continuations)
for the complete contract.
