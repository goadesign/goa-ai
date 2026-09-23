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

To keep frequently used tools immediately available within the same compiled
toolset, name only the tools that should load through search:

```go
Use(Records, func() {
    Deferred("search", "analyze")
})
```

Here `Records` defines `lookup`, `search`, and `analyze`. The consumer advertises
`lookup` immediately and loads the other two through search. Names must exactly
match authored local tool names, such as `"search"`, not qualified runtime IDs
such as `"records.search"` or generated Go names. This works for local tools,
agent tools, external MCP tools with declared schemas, and Goa-backed MCP tools.
Other consumers and shared tool definitions keep their own loading behavior.

Empty or duplicate names are rejected, including duplicates across declarations.
Multiple named declarations combine their selections. Repeated `Deferred()` is
valid, but combining it with any named declaration is rejected. The generator
rejects unknown names after collecting the complete tool list, including tools
defined by a Goa MCP service.

Dedicated pagination tools retain their existing runtime behavior: they are
hidden until a query has another page, then the runtime advertises an immediately
available continuation action. Deferring the query or its dedicated continuation
tool does not defer that generated action.

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

Named `Deferred` selections are rejected for both `FromRegistry` toolsets and
whole registries because their tools are unknown during generation. Use
`Deferred()` to load all their tools through search, or omit it to advertise
them immediately.

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

Generated toolset packages expose `Toolset()` for the complete authored declaration
and `ToolSchemas()` for its tools. The declaration uses the generated registration
name, such as `"orchestrator.helpers"`. Both return fresh owned values. Use `ToolSchemas()` for
`RegisterPayload.Tools`, together with the generated schema fingerprint and
the provider registration lifecycle. Register sends the definitions during
startup. The required `Registration.Renew` callback then calls `RenewProvider`
with the toolset, provider ID, incarnation ID, and original registration token;
it returns only the granted duration. Renewal never uploads definitions or
returns a new token. See the [complete provider example](runtime.md#registry-routed-provider-execution-service-side).

The registry keeps current definitions separate from compact provider state
and permanent retired tokens. Lifecycle and health operations read compact
Redis state directly. Definition readers compare fingerprints before fetching
bytes; tool calls reuse compiled execution schemas. This internal reuse does
not change what dynamic consumers resolve or how long their catalog is valid.

An active provider that loses its registration or exact lease stops with
`provider_lease_lost`; it does not Register again. Temporary connection failures
can retry within the existing lease cutoff, and stream/group or ping-lease loss
can recover while durable catalog authority remains intact. The runtime guide
owns [recovery and shutdown details](runtime.md#registry-routed-provider-execution-service-side).

Each schema includes a generated `ConsumerContract`: title, search word counts,
field and union metadata, labels, result handling, confirmation, pagination,
and server-only data declarations. Consumers validate that record and compile
JSON codecs for the received schemas. Numbers retain their exact JSON value.
References must resolve inside the supplied schema; schema compilation does
not fetch files or remote resources.

Dynamic execution supports service tools and [native Agent tools](#register-agent-tools),
including confirmation, bounded results, declared pagination partners, and
server-only result data. Terminal/bookkeeping tools and planner control
integration remain compiled dependencies. An old schema-only registration or an
unsupported execution kind fails resolution explicitly.

Runtime-authored Go tools use the same `genregistry.ToolSchema` declaration.
`runtime/toolregistry/contract.Compile` applies generated declaration validation
and creates an owned `tools.ToolSpec` with validating JSON codecs. Supply the
schemas and metadata explicitly, including field paths, search terms, required
labels, confirmation, and result handling. The compiler does not infer injection
or permissions from field names. Generated service tools continue using their
generated typed codecs; `runtime/toolregistry/schema.Codec` is available when a
runtime-authored provider needs a JSON codec for one schema. Use
`contract.Fingerprint(toolset)` to calculate `RegisterPayload.SchemaFingerprint`
for runtime-authored declarations or the complete `Toolset()` value, including
its description and tags. The function uses the same algorithm as the registry
and generated fingerprints; the registration timestamp does not affect identity.
The precomputed `SchemaFingerprint(name)` helper continues to describe the
existing provider registration without optional toolset-level annotations.

To accept these declarations through your own Goa API, import
`goa.design/goa-ai/registry/design/types`. It exports the registry's existing
schemas, including `ToolSchema`, `ConsumerContract`, `AgentToolsetDeclaration`,
and `ToolCallMeta`, without registering the registry API or service:

```go
import (
    registrytypes "goa.design/goa-ai/registry/design/types"
    . "goa.design/goa/v3/dsl"
)

var _ = Service("catalog", func() {
    Method("register_agent_tools", func() {
        Payload(registrytypes.AgentToolsetDeclaration)
        GRPC(func() {})
    })
})
```

Goa generates your API's transport validation from the same schemas used by
the registry. Registration and execution still use the registry's generated
client and runtime contracts. Existing imports from `registry/design` remain
supported for designs that intend to include the registry service.

## Register Agent tools

A native Agent tool describes a child workflow, not a Pulse service provider.
Its `ConsumerContract.Kind` is `"agent"`, and `ConsumerContract.Agent` contains:

- `Executor`: the ID of a worker configured before runtime startup.
- `Configuration`: an immutable application reference, such as
  `"installer/revisions/7"`, that identifies the prompt, model, and tool policy.

A complete result contract is required. The model supplies the tool's domain
arguments; it does not choose the executor, configuration, workflow name, or
queue. The application owns configuration storage and must keep a referenced
revision available for accepted calls that have not yet prepared their child.

Register the tools through the clustered registry's generated client:

```go
registered, err := registryClient.RegisterAgentToolset(ctx, &genregistry.AgentToolsetDeclaration{
    Name:  "installation",
    Tools: declarations,
})
if err != nil {
    return err
}
```

Here `declarations` contains complete `*genregistry.ToolSchema` values. Each Agent
target is part of the registration's fingerprint. No provider ID, Pulse stream,
lease renewal, or provider health ping is needed. Registration does not deploy
or start the executor worker.

Repeating an identical active registration succeeds. To change it, call
`ReplaceAgentToolset` with `*genregistry.ReplaceAgentToolsetPayload`, including
`ExpectedRegistrationToken: registered.RegistrationToken`. A stale token returns
`admission_conflict`. `Unregister` removes the current declaration from discovery;
its token can be used to reactivate it through `ReplaceAgentToolset`.

Service and native Agent registrations cannot overwrite one another. Service
providers continue using `Register`, their generated fingerprint, and the
existing lease lifecycle. Calling a native Agent through `CallTool` or
`CallResolvedTool` is rejected; the Agent runtime starts a child workflow.

### Prepare the child configuration

Before sealing the consuming runtime, register an `AgentToolResolver` for each
native executor. The callback receives the saved configuration reference and a
copy of the validated tool call. It returns `*runtime.AgentToolConfiguration`:

```go
err := rt.RegisterAgentToolResolver(gengeneric.AgentID,
    func(ctx context.Context, revision string, call *runtime.ToolCall) (*runtime.AgentToolConfiguration, error) {
        // Application code loads this exact revision and builds its transcript
        // using call.Payload. Model and product choices can travel in Labels;
        // enforceable tool restrictions belong in Policy.
        return configurations.Prepare(ctx, revision, call)
    },
)
if err != nil {
    return err
}
```

The returned configuration contains `Messages`, `Labels`, `Policy`, and optional
`RenderedPrompts`. Its labels extend or replace inherited parent labels. The
application is responsible for its facility and product scope rules. The runtime
owns the child run ID, session ID, parent links, and original tool arguments.
Preparation runs in an activity. Workflow replay uses the recorded configuration;
a continuation uses the saved checkpoint instead of resolving the revision again.

An Agent definition already permits its own worker and its generated child
workers. This supports many saved configurations running on one generic worker.
To permit an additional worker in a programmatically assembled definition:

```go
executor := gengeneric.Definition()
consumer := genassistant.Definition().WithAgentExecutors(&executor)
```

`WithAgentExecutors` copies the supplied definitions and returns a new immutable
definition. Use that same definition for `AgentRegistration.Definition` on the
worker and `rt.ClientFor(consumer)` in callers. The generated registration and
client helpers continue using their original generated definition. A registry
target outside the allowed workers fails discovery before model invocation.

### Return the selected result

Both `planner.PlanInput` and `planner.PlanResumeInput` expose `ParentTool` for a
nested run. Its `Result` contains the exact result schema and codec selected by
the parent. A generic planner can use this schema with `model.StructuredOutput`
in its final model call, then return `planner.FinalToolResult`. Structured output
and tool calls use separate model requests under the existing model contract.
Top-level runs have no `ParentTool`.

The runtime validates the final JSON against that retained contract and links the
result to the child run. A native Agent tool that returns only a chat message is
rejected. Compiled Agent tools retain their existing result behavior.

Registry replacement affects subsequent planning activities, including later
turns in an existing session. It cannot change an accepted call's configuration,
result schema, or pending approval. Child progress, cancellation, clarification,
and approval use the normal child workflow and continuation machinery.

## Select sources using application scope

An application can implement `runtime.RegistryTools` and attach it with
`AgentDefinition.WithRegistryTools`. During `Resolve`, `catalog.RunLabels()`
returns an owned copy of the current run labels. Use these to select product
registrations through `IncludeToolset` or `IncludeRegistry`. `Allows` declares
the sources that saved calls may continue consuming. Existing run policies
still restrict the tools advertised and executed.

For example, an application can use its own `namespace` label to select
support tools for one session and operations tools for another. Goa-AI
does not define namespaces or infer authorization from label names. Catalogs
belong to individual planning activities, so concurrent sessions do not change
one another's tool lists.

## Upgrade for native Agent tools

Upgrade the registry, then the executor workers and every consumer that can
discover these toolsets. Publish native declarations only after those upgrades;
older whole-registry consumers reject Agent declarations during discovery. Existing service
fingerprints, provider wire protocol, and stored service registrations are
unchanged by this addition. Older registries cannot read native records, and
older workers cannot restore native child checkpoints; remove native records
and finish their runs before rolling back to those versions. Any earlier
[registry storage upgrade](runtime.md#registry-storage-upgrade) still applies.

Regenerate tool packages to obtain `Toolset()`. Existing `ToolSchemas()` callers
and generated service codecs keep their existing contracts. The published
compiler and planner input fields are additive; `PlannerContext` is unchanged.

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

Using named deferral requires regenerating the consumer. Existing `Deferred()`
calls retain their whole-toolset or whole-registry behavior. Named choices emit
the same static deferred-ID argument already consumed by the runtime; they add
no provider API or persisted state.

`Deferred` now has type `func(...string)`, which is not assignable to `func()`.
Replace `Use(Records, Deferred)` with `Use(Records, func() { Deferred() })`,
and wrap other `func()` callback assignments the same way. Calls to `Deferred()`
remain valid.

Regenerate providers and consumers with Goa v3.32.0. Replace generated
startup `Discover` calls, `RegistryToolsets` inputs, and dynamic executor wiring
with `RegisterRegistry`. Publish generated `ToolSchemas()` records before
turning on dynamic consumers. Upgrade the registry to serve `ResolveToolset`
and `CallResolvedTool`; older registry servers cannot serve this consumer path.
Old registrations without `ConsumerContract` remain usable by their existing
static integrations but are rejected by dynamic consumers.

The [registry storage upgrade](runtime.md#registry-storage-upgrade) is separate
from consumer loading and earlier wire-version migrations. It requires the
new Renew callback and an offline conversion with all old writers stopped.
Wire protocol 10, schema fingerprints, saved calls, absolute expiry, and
permanent retirement history remain intact. Do not reset current catalog data
to adopt the new layout. The preview guide lists source changes, including
removed provider error symbols, and the prerequisites for the conversion
artifact being prepared.

Confirmation templates now read canonical JSON names: change `{{ .Key }}` to
`{{ .key }}` and use `{{ json .value }}` when inserting JSON values.
Use `index` for optional properties. This applies to static and dynamic tools;
it makes templates independent of a provider's generated Go type names.

Local tests cover SDK request/response encoding, streaming, history,
compaction, generated consumers, catalog changes, exact admission, and saved
execution contracts. Live model search quality and token savings require
deployment-specific evaluation; local tests do not establish those results.
