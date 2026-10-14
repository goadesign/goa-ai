# Run subjects

A run's subject identifies the work that owns its records. The generated
`registry/gen/runidentity.RunSubject` selects exactly one executor:

- an actual Agent identifier; or
- a service tool's configured Registry source and qualified tool name.

The subject remains fixed for the run. It does not grant execution permission,
select a provider lease, or replace the exact tool declaration and arguments.
A service tool has no synthetic Agent identity or hidden Session.

## Construction and validation

Agent clients and direct Agent operation APIs construct their own Agent subject.
Low-level adapters and custom Stores use the generated constructors:

```go
agentSubject := genrunidentity.RunSubject{
    Executor: genrunidentity.NewRunExecutorAgent("catalog.agent"),
}
serviceSubject := genrunidentity.RunSubject{
    Executor: genrunidentity.NewRunExecutorServiceTool(
        &genrunidentity.ServiceToolSubject{
            Registry: "primary",
            Tool: "catalog.lookup",
        },
    ),
}
```

Import `goa.design/goa-ai/registry/gen/runidentity` as `genrunidentity`.
For a variable of another string type, convert to the generated
`RunExecutorBranchAgent` type before calling the Agent constructor.

Use `EncodeRunSubject` to validate an existing typed value and `DecodeRunSubject`
to read its complete original JSON. The union's `Validate` method checks branch
selection only; it does not replace the complete generated subject codec.
The schema rejects an unset executor, an empty Agent, a missing Registry source,
and a tool without nonempty text on both sides of its final dot. The decoder
also rejects unknown fields, duplicate fields, and combined branches.

Common run inputs, outputs, tool activity inputs, starts, metadata, records,
seeds, preparations, hook events, and snapshots preserve this exact generated
value. A record for another subject cannot join a run, even if its run and
Session identifiers match. Compare subjects by value, including service fields;
the service branch contains a pointer, so pointer identity is not value equality.
Stores and retained events copy mutable service values before exposing them.

Planning, Agent clients, model-history seeds and checkpoints, and child Agent
identifiers remain Agent-specific. They explicitly require an Agent subject.
A service run can have lifecycle and tool records without model messages.
This common representation alone does not add a standalone service-tool start API.

## Upgrade required

This change removes `AgentID` from common run envelopes and replaces it with
required `Subject`; there is no empty-field fallback or dual write. Update every
producer and consumer, including custom Stores, event subscribers, snapshots,
generated service mappings, byte accounting, and tests. Keep actual Agent-only
fields as Agent identifiers. A protobuf message field must use a fresh field
number rather than reuse a released string field number.

Before switching persisted callers, inventory saved run metadata and records,
initial-history seeds, prepared requests, accepted engine requests and their
original digests, suspension checkpoints, and retained completion histories.
An Agent-only historical row can be converted only when its owning system can
prove the actual Agent executor. The framework does not infer one. Preserve
relationships, record ordering, original results, and accepted request identity;
changing encoded input changes its digest and must not silently create a new
accepted operation.

Top-level RunOutput now uses `json/goa-ai-run-output-v3`; earlier encodings are
rejected. Nested saved ToolCall values also carry Subject, so current format
names alone do not prove replay compatibility. Drain or retain work on its
matching old owner, or perform an explicit owner-controlled data conversion
with replay verification. Include parent histories and exported histories, not
only child executions. Coordinate all independently deployed readers and
writers before activation. See [Workflow results](workflow-results.md).

Rollback requires matching data and execution formats. Do not revert only the
binary after new subjects or completion results have been written. Any temporary
migration tooling belongs to the application and must be removed after verified
cutover; it is not a second permanent runtime contract.
