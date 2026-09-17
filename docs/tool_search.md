# Tool search and dynamic registries

Tool search lets a model load tool definitions when it needs them. A registry
lets providers change the available tools without rebuilding the consuming
agent. These are independent choices: a generated static catalog can use tool
search, and a registry catalog can be advertised immediately.

## Declare what the agent consumes

For a large, compiled toolset, add `Deferred()` to its consuming `Use`:

```go
Agent("assistant", "Answer questions and perform requested work.", func() {
    Use(Records, func() { Deferred() })
    Use(Clarification)
})
```

`Deferred` changes loading, not permission or execution. It belongs to the
consumer, so another agent can consume `Records` immediately. Put it inside
`Use`, never on a shared `Toolset` definition or an `Export`.

For changing provider tools, reuse a registry:

```go
var Company = Registry("company", func() {
    URL("https://registry.example")
})

var Records = Toolset(FromRegistry(Company, "records"))

var _ = Service("assistant", func() {
    Agent("reader", "Use the records provider.", func() {
        Use(Records, func() { Deferred() })
    })
    Agent("generalist", "Use the company catalog.", func() {
        Use(Company, func() { Deferred() })
    })
})
```

The reader resolves one required toolset. The generalist lists and resolves
every current toolset in that registry. Removing `Deferred()` makes the same
catalog immediately visible to the model. No namespace resource is needed.
A `Version("1.2.3")` inside the named toolset or its consuming `Use` requires
that exact published version; it does not select an archived version.

Within one agent, duplicate sources and overlapping named/whole-registry
consumption are rejected during DSL evaluation. A registry reference cannot
declare inline tools, select an inline subset, or be exported: its provider
owns those contracts. Consumer tag overrides and `PublishTo` on registry
references are also rejected. Existing run policy filters the resolved tools before
they reach the model.

## Connect the registry once

Construct the clustered registry's generated service client and the Pulse
result-stream client in the application's startup code. Register the connection
under the DSL name before sealing the runtime or starting a run:

```go
// registryClient is *genregistry.Client from
// goa.design/goa-ai/registry/gen/registry.
// pulseClient implements features/stream/pulse/clients/pulse.Client.
if err := rt.RegisterRegistry("company", registryClient, pulseClient); err != nil {
    return err
}
if err := genreader.RegisterReaderAgent(ctx, rt, genreader.ReaderAgentConfig{
    Planner: myPlanner,
}); err != nil {
    return err
}
client := genreader.NewClient(rt)
```

Registry tools need no handwritten executor, discovery call, or
`RegistryToolsets` input. Register any compiled toolsets through the existing
generated `RegisterUsedToolsets` helper. `Definition()` and `NewClient(rt)` have
the same signatures for static and dynamic agents and perform no network reads.
Parent and child agents retain their own declared registry consumption.

The generated HTTP catalog clients are a separate integration surface for a
matching HTTP catalog server. They do not replace the clustered registry's
generated service client in `RegisterRegistry`.

## Publish complete provider contracts

Generated toolset packages expose `ToolSchemas()`. Use that factory for
`RegisterPayload.Tools`, together with the generated schema fingerprint and
the existing provider registration lifecycle.

Each schema includes a generated `ConsumerContract`: title, search word counts,
field and union metadata, labels, result handling, confirmation, pagination,
and server-only data declarations. Consumers validate that record and compile
JSON codecs for the received schemas. Numbers retain their exact JSON value.
References must resolve inside the supplied schema; schema compilation does
not fetch files or remote resources.

Dynamic execution supports service tools, including confirmation, bounded
results, declared pagination partners, and server-only result data. Child-agent
tools, terminal/bookkeeping tools, and planner control integration remain
compiled dependencies. An old schema-only registration or an unsupported
execution kind fails resolution explicitly; it is not silently downgraded.

## Keep planners small

Planners continue to use the runtime's filtered definitions:

```go
mc, ok := input.Agent.PlannerModelClient("primary")
if !ok {
    return nil, errors.New("primary model is not registered")
}
summary, err := mc.Stream(ctx, &model.Request{
    Model:     configuredModelID,
    Messages:  input.Messages,
    Tools:     input.Agent.AdvertisedToolDefinitions(),
    MaxTokens: 4096,
    Stream:    true,
})
if err != nil {
    return nil, err
}
if len(summary.ToolCalls) > 0 {
    return &planner.PlanResult{ToolCalls: summary.ToolCalls}, nil
}
return &planner.PlanResult{FinalResponse: summary.FinalResponse()}, nil
```

The output budget in this example is an application choice. OpenAI search
requires a positive request `MaxTokens` or adapter `MaxCompletionTokens`;
all native search rounds consume that logical invocation's output budget.

The planner never executes a search tool or expands definitions. Native search
calls remain inside the model adapter. Only ordinary application tool calls
return to the runtime.

## Provider behavior

| Adapter | Search and loading |
| --- | --- |
| OpenAI Responses, direct or Bedrock | The model emits native client search calls. The adapter uses BM25, a word-based relevance ranking, over the permitted tools' generated names, titles, and descriptions. Matching definitions return through native search output. |
| Anthropic Messages | The adapter sends the permitted catalog with deferred-loading flags and Claude's hosted regex search tool. Anthropic performs search and definition expansion. |
| Claude Messages on Bedrock | The same behavior uses `NewAnthropic`, the InvokeModel transport, and Bedrock's search declaration and beta. The Converse adapter does not implement search. |
| Other adapters | Requests requiring unimplemented search fail with `model.ErrToolSearchUnsupported`; there is no eager-loading fallback. |

For OpenAI, the initial search declaration contains only a query argument.
It contains no directory of names or descriptions. The complete deferred
catalog stays in the application, and each search returns a bounded ranked
selection. For Claude, the complete catalog is transmitted to the provider,
which manages deferred context loading. These have different network and
token-accounting implications; measure the actual chosen model.

Each OpenAI search result places the selected function inside a native namespace
with the same provider name. This makes Bedrock return a complete function-call
identity that can be replayed. The adapter owns this wire representation; it
requires no namespace DSL, application mapping, or separate loaded-tool state.
Saved function-call metadata preserves the returned namespace unchanged.
Requests with tools loaded eagerly retain their existing representation.

Bedrock histories created with bare dynamically loaded functions may contain
calls without a namespace. Bedrock rejects those calls when replayed. Start a
new conversation or deliberately remove the complete affected exchange through
history policy; the adapter does not invent missing provider fields.

Forcing one named tool makes that tool immediately available for that request.
Disabling tools suppresses new discovery. Provider model and endpoint support
must be checked in the deployment using them.

## Catalog lifetime, execution, and history

At the start of each planning activity that can initiate work, generated code
reads that agent's declared sources. The resulting catalog stays fixed through
that activity's model calls and native search rounds. Another activity reads
again, so a provider registered between turns becomes available without
restarting the consumer. Final-answer-only and explicit finalizer activities
do not read the registry.

An empty whole registry is valid. A missing named registration, version
mismatch, failed registry read, duplicate tool identity, or toolset removed
between listing and resolution fails the activity. There is no stale-catalog
fallback. Concurrent activities do not share a mutable discovered-tool map.

Once a call is accepted, the runtime saves its selected definition, any fixed
pagination partner, and the registry's existing registration token. It does
not save the entire catalog. Execution, confirmation, result decoding, and
checkpoint restoration use that saved contract. `CallResolvedTool` requires
the same registration token before publication. If the provider registration
has been replaced before publication, the registry records `call_not_admitted`
instead of sending old arguments to a new contract. Overload retry retains the
same token and returns `admission_conflict` if that admission was replaced.
Already published calls keep their original assignment and result.

Native model search history is stored in existing message metadata, in its
original order. Keep that metadata when copying or persisting messages.
Built-in history policies preserve retained native records; there is no
separate loaded-tool store to synchronize during compaction. Saved definitions
explain historical calls but do not authorize new ones: current consumption,
run policy, and registration admission still apply.

Claude's native add/remove history requires a model supporting tool
availability changes. Replacing a retained tool definition under the same
name cannot be represented by that protocol and is rejected explicitly.
An application must start a new conversation or supply a deliberately
compacted conversation that no longer retains that definition. The adapter
does not silently reset history. Native-only Claude pause continuation is not
implemented in this release.

## Generation and upgrades

The generator emits literal search word counts, complete provider contracts,
static deferred IDs, direct source reads, and exact source/version permission
checks. Runtime code handles only changing catalogs, model queries, returned
data, and execution state. Neither applications nor generated agents interpret
the DSL at runtime.

Regenerate providers and consumers with Goa v3.31.1. Replace generated
startup `Discover` calls, `RegistryToolsets` inputs, and dynamic executor wiring
with `RegisterRegistry`. Publish generated `ToolSchemas()` records before
turning on dynamic consumers. Upgrade the registry to serve `ResolveToolset`
and `CallResolvedTool`; older registry servers cannot serve this consumer path.
Old registrations without `ConsumerContract` remain usable by their existing
static integrations but are rejected by dynamic consumers.

Confirmation templates now read canonical JSON names: change `{{ .Key }}` to
`{{ .key }}` and use `{{ json .value }}` when inserting JSON values.
Use `index` for optional properties. This applies to static and dynamic tools;
it makes templates independent of a provider's generated Go type names.

Local tests cover SDK request/response encoding, streaming, history,
compaction, generated consumers, catalog changes, exact admission, and saved
execution contracts. Live model search quality and token savings require
deployment-specific evaluation; local tests do not establish those results.
