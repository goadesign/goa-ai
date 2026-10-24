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
Redis state directly. Definition readers fetch and validate an atomic snapshot
of the current definition and state; tool calls reuse digest-keyed compiled
execution schemas. No per-name definition cache substitutes earlier ordering
when equivalent content is replaced. These reads do not change what dynamic
consumers resolve or how long their catalog is valid.

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

For bounded results, providers return result data as JSON and send counts,
truncation, and paging information separately in `Bounds`. Like generated typed
codecs, the portable result codec accepts both provider data and finalized
results. Fields supplied by the runtime are optional in the codec and checked
when present; the runtime adds their authoritative values after encoding.
Advertised schemas still require counts and truncation in finalized output,
including `total` when the bound method guarantees it. Schema-only clients
continue validating the advertised schema.

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
client and runtime contracts. If your design imported these declarations from
`registry/design`, update that import to `registry/design/types`. Generated
registry client types keep their existing package and names. Shared unfinished-tool
operation types use the `ToolOperation` prefix, such as
`ToolOperationExecutionContinuation`, so applications can declare domain types
such as `TaskInput` in the same Goa design. Regenerate consuming APIs when
upgrading: the generated Go and protobuf type names change; field numbers,
field names, union branch names, and saved operation JSON remain unchanged.

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

Before sealing the provider runtime, register an `AgentToolResolver` for each
Agent whose exports are generated. For exports owned by immutable saved revisions,
use [RegisterAgentOperationProvider](runtime.md#revision-owned-operation-contracts)
instead; that section defines the independent declaration check. Consumers do not
register this resolver or load the provider definition. The callback receives the
original generated execution invocation,
the saved configuration reference and a copy of the validated tool call.
`call.Name` identifies the original provider operation, including when a
registered import derives a different model-visible name. `call.TranscriptName()` and
`call.TranscriptPayload()` retain the model's name and input; `call.Payload`
contains the complete validated input after consumer-owned values were added.
The callback returns `*runtime.AgentToolConfiguration`:

```go
err := rt.RegisterAgentToolResolver(gengeneric.AgentID,
    func(ctx context.Context, origin genregistry.ExecutionInvocation, revision string, call *runtime.ToolCall) (*runtime.AgentToolConfiguration, error) {
        // Application code loads this exact revision and builds its transcript
        // using call.Payload. Model and product choices can travel in Labels;
        // enforceable tool restrictions belong in Policy.
        return configurations.Prepare(ctx, origin, revision, call)
    },
)
if err != nil {
    return err
}
```

The returned configuration contains required `History` plus planner metadata,
execution labels, policy and optional rendered-prompt facts. Select `runtime.LiteralAgentHistory(messages)` or
`runtime.NextTurnAgentHistory(completedRunID, systemMessages, freshMessages)`.
The [provider history contract and upgrade](runtime.md#provider-owned-history)
explain exact source ownership, reasoning exclusion and the replacement of the
previous `Messages` field. Its labels extend or replace inherited parent labels.
The application is responsible for its facility and product scope rules. The runtime
owns the child run ID, session ID, parent links, and original tool arguments.
Preparation runs in an activity. Workflow replay uses the recorded configuration;
a continuation uses the saved checkpoint instead of resolving the revision again.

Each registered Agent target supplies its executor ID, workflow name, worker
queue and immutable configuration reference. The provider checks those route
values against its own registered Agent before preparing the input. Consumers
retain this operation contract and route; they do not import the provider
implementation or its private tool definitions. Static child Agents declared
with the DSL continue using their generated definitions.

When a provider suspends for external tool results, the checkpoint retains only
those requested result schemas and bounded-result contracts. The parent checks
the response before creating its successor, without loading private tools. The
provider still validates its complete saved planner state with its generated
codecs before executing. Cancellation follows the exact saved child links;
another run in the same session is not a valid cancellation target.


### Return the selected result

Both `planner.PlanInput` and `planner.PlanResumeInput` expose `ParentTool` for a
nested run. `ParentTool.Name` identifies the original provider operation, so
planners use it to select operation-specific behavior. `RunContext.Tool` retains
the name selected by the caller, including a derived registered-resource name; it keeps
saved registry lookup and child run identity stable. `ParentTool.Result` contains
the exact result schema and codec selected by the parent. A generic planner can
use this schema with `model.StructuredOutput` in its final model call, then return
`planner.FinalToolResult`. Structured output and tool calls use separate model
requests under the existing model contract. Message-based runs have no
`ParentTool`.

The runtime validates the final JSON against that retained contract and links the
result to the child run. A native Agent tool that returns only a chat message is
rejected. Compiled Agent tools retain their existing result behavior.

Registry replacement affects subsequent planning activities, including later
turns in an existing session. It cannot change an accepted call's configuration,
result schema, or pending approval. Child progress, cancellation, clarification,
and approval use the normal child workflow and continuation machinery.

## Select sources using application scope

The host authenticates the caller and authorizes registered resource owners
before exposing or invoking their operations. Registry's `CatalogIdentity`
records the owning scope and permanent resource name. `ListCatalogResources`
reads a bounded page for one owning scope. A selected registration retains
its original declarations without granting access. Invocation identity, labels, and catalog
membership are never credentials. Registry does not retain end-user permission
decisions or sharing relationships.

An application can implement `runtime.RegistryTools` and attach it with
`AgentDefinition.WithRegistryTools`. During `Resolve`, `catalog.RunLabels()`
returns an owned copy of the current run labels. Use these to select product
registrations through `IncludeToolset` or `IncludeRegistry`. `Allows` declares
the sources that saved calls may continue consuming. Existing run policies
still restrict the tools advertised and executed.

When an application has already selected an exact registration, `IncludeResolved`
imports it without another read and preserves original operation names. Use
`IncludeRegistered(registry, source, resolved, deferred)` when multiple owners or
resources can supply the same operation. Its required `CatalogSource` identifies
the logical owner, resource and exact native revision or service registration.
The application authorizes that selection; the runtime validates its declaration
route, token and operation kind. It never interprets a private route as an owner.

Registered names retain the original operation around a separate identity segment:
`originalToolset.originalOperation.hexRegistry_kind_hexOwner_hexResource.originalOperation`.
The original operation appears first for provider name projection and last to
preserve the qualified tool identifier's operation leaf.
Each hex field encodes the exact UTF-8 bytes, so separators are unambiguous.
Resource revisions and service replacement tokens do not affect the name. No
caller supplies aliases, and no additional catalog record is stored. Existing
provider adapters apply their own name projection and reject collisions.

`ForEachRegisteredSource` visits each distinct selected source once and supplies
independent copies. Generated `Bind*SourceInput` helpers attach application-owned
input only when registry, owner, resource, target kind, route and registration
all match. Static `Bind*Input` helpers retain their existing behavior.

For example, an application can use its own `namespace` label to select
support tools for one session and operations tools for another. Goa-AI
does not define namespaces or infer authorization from label names. Catalogs
belong to individual planning activities, so concurrent sessions do not change
one another's tool lists.

## Inspect active registration facts

Applications that own catalog resources can iterate their scope with
`registry.Service.CatalogRoutesAfter` and read each route with
`ReadCatalogRegistration`. The latter returns the saved owner, exact registration
token and service-or-native kind without reading declarations or provider health.
It returns `not_found` when the route has retired since listing. The exclusive
lexical continuation is a saved route name, so process restarts do not change its
position. These observations grant no execution authority and create no
reservation. `ReadCatalogIdentity` remains available when ownership is needed
after retirement. See the [catalog storage contract](../DESIGN.md)
for the owner and registration rules.

## Bind generated service tools

When an application selects a registration for tools already compiled into an
Agent, call `catalog.BindStaticService(registry, expected, selected, readPrevious, names...)`
from its `RegistryTools.Resolve` implementation. Supply the generated `Toolset()`
as `expected`, the complete owner-authorized resolution as `selected`, and a
nonempty list of original generated tool identifiers. There is no implicit
"bind all" choice.

`readPrevious` is required and has type
`func(context.Context, string, string) (*genregistry.ResolvedToolset, error)`. Its string arguments are the saved
private toolset route and exact registration token, in that order. Capture the
current resource's owner and permanent name in this callback. Use the resource
owner's API to verify that the saved route and token belong to that same resource;
return an error for another resource or a failed read. Matching operation names
or schemas do not establish resource ownership. The callback returns `(*genregistry.ResolvedToolset, error)`: the exact old
registration already fetched from the owner. The runtime verifies the saved
selected-operation fingerprint with those declarations, then checks the new
page with current inputs, paging rules and policy. Description changes alone
do not remove an otherwise valid page. Accepted calls and retries keep their
original selection. The callback runs in the current
planning activity and is not stored in calls or checkpoints.

The runtime compares the complete portable service declaration and version,
allowing only the expected toolset name to become the selected route. It checks
each named tool against the consuming Agent's generated contract and its
`RegistryTools.Allows` predicate. Native Agent operations and tools with
local-only execution semantics cannot use this binding. `IncludeResolved`
continues to reject static name collisions; use the explicit binding method for
this case.

### Reuse a checked service declaration

If the complete expected and selected declarations stay fixed across planning
activities, call `runtime.CompileStaticService(expected, selected)` once. It
performs the same generated JSON conversion, complete validation and declaration
comparison as raw service binding, including unselected operations. The returned
`*runtime.CompiledStaticService` owns only compact operation and paging-partner
selections. Changing either raw input afterward cannot change that value.

Each activity then calls
`catalog.BindCompiledStaticService(registry, compiled, readPrevious, names...)`.
It checks the current registry connection, consumption predicate, generated
operation declarations and local specifications before installing any selection.
Supply the current activity's `readPrevious`; the compiled value retains no
callback, actor, permission, policy, application state, provider health or full catalog.
Input preparation and generated execution still use the same activity-owned
catalog described below.

Compilation does not select a provider or grant execution authority. If current
selection changes to another registration, compile that new result; retaining an
old compiled value does not make its provider current. Accepted calls and retries
continue to use their saved selection. Cold processes compile again from their
owner-selected declarations; the value has no serialized format or durable store.
Keep `BindStaticService` when the supplied raw values may change: it still checks
them completely on every call. Concurrent readers can share a compiled value,
but callers must not mutate either raw input while it is being compiled.

Register the generated toolsets with an executor constructed by
`executor.NewResolved(registry, client, pulse, specs, options...)`, where `specs`
is the generated specification lookup. Generated model names, deferred loading,
input preparation, result codecs, hints, and materializers remain in use. The
binding changes only which exact service registration receives execution.
The complete static declaration and the Agent's executable tool list serve
different purposes: binding validates the declaration, but only executable tools
become planner choices. Binding an omitted operation does not advertise or permit
new calls to it, on initial planning or resume. Separately discovered tools remain
available under their imported names.
Every call routed to `NewResolved` must have a saved binding. Selecting only some
operations does not install a separate executor for each name. If a generated
toolset also contains unbound operations, application executor composition must
route those operations to their existing executor.
`BindInput` may prepare generated inputs after the service binding is installed.
The original model arguments remain separate from the completed execution input.

The existing saved `ToolCall.Registry` retains the selected registration without
copying the tool's declaration. For a bound tool it saves the registration token,
the registered toolset name, description, version, tags, and registration time,
the sorted names of the called operation and its paging partner, a SHA-256
fingerprint of their generated declarations without provider Agent targets, and,
for a native Agent operation, the selected worker route and configuration. A
saved call therefore stays a few hundred bytes however large the tool's schemas
are. Every reader restores the complete selection from the consuming agent's
generated declarations, which generated definitions supply with
`WithRegistryDeclarations`. Both initial execution and overload retries call
`CallResolvedTool` with that route, original operation, token, and invocation
identity. Replacing the current registration cannot redirect an accepted call.
A new page request selects the current catalog registration after verifying
that every prior page belongs to the same resource. Its original query arguments,
next cursor, and runtime source-call ID stay unchanged. Independent source calls
remain separate even when their query and cursor bytes are equal. Accepted page
requests and their retries retain their own exact registration. Removed paging
partners, changed cursor or retained-field declarations, or incompatible current
arguments supply no action; owner-read failures
stop planning and remain errors. Availability checks use the same current catalog
selection as planning. New native Agent pages likewise select the current
registration and configuration; accepted child calls retain their own selection.
Finalization and synthesis skip catalog discovery while retaining existing static
input preparation.

Unfinished compiled paging still requires an exact match to the saved generated
declaration fingerprint, including result schemas and metadata. Selecting a new
provider does not migrate a changed declaration, even when its input alone still
matches. Such current work rejects the changed contract; a suspension that still
consumes it cannot resume, and unfinished history from an earlier turn supplies
no next-page action. Completed results and closed queries remain raw history.
These are the same outcomes as for a call whose saved full declaration differs
from the current generated contract. Calls saved with their full declaration by
earlier releases keep their existing behavior. An agent that does not compile the
tool cannot interpret a call saved this way.

Binding requires agents generated with registry declarations. `BindStaticService`
and `BindStaticAgent` reject a tool whose saved call cannot be restored from the
consuming definition's generated declarations; regenerate the agent to fix it.

`catalog.SelectedOperation()` returns a fresh copy of the native operation
retained in the current run. It reads a direct operation or the saved registry
selection, including an imported name, without consulting current registrations.
A missing or malformed selection is an error. Continued runs retain the same
operation. Applications may use this declaration to select its service
requirements; the model does not repeat configuration or registration tokens.

No wire or storage migration is required to add bindings to an unchanged
generated service definition. Before replacing an existing executor with
`NewResolved`, drain accepted unbound calls even when the generated definition
is unchanged: those saved calls have no registration for the new executor.
A direct saved registry selection has no marker
that distinguishes dynamic execution from bound static execution: the consuming
Agent's definition makes that distinction. Changing a tool between these forms,
or removing its static declaration, requires draining affected runs before
changing the definition. Reverting such a change requires the same care. A
same-named static tool owned by another Agent never changes the call's executor.

## Bind generated native Agent tools

Call `catalog.BindStaticAgent(registry, expected, selected, readPrevious, names...)` with the
complete generated unbound operation declarations, the owner's selected
`ResolvedToolset`, and every original generated tool name. The runtime compares
all operation schemas and policy and checks the provider executor, workflow and
queue against the consuming definition's generated child route. Registration
configuration may change between revisions; each accepted call retains its
selected revision. Toolset discovery descriptions and tags do not alter the
operation contract.

The required `readPrevious` has type
`func(context.Context, string, string, *genregistry.AgentToolTarget) (*genregistry.ResolvedToolset, error)`.
Its arguments are the prior private toolset route, exact registration token,
and selected Agent target. Use the target's saved configuration reference and
existing owner APIs to prove that the old registration belongs to the same
owner and permanent resource as the current selection. Failed reads and another
resource remain errors. Return the exact old registration already fetched by
that proof; the runtime verifies its saved selected-operation fingerprint.
For a newly selected page, the current registration
supplies the new child configuration; the original query and cursor stay intact.
Accepted child calls keep their saved configuration and do not run this check.

Binding the complete native export set does not make every export executable by
the consuming Agent. Its executable list still chooses the static planner tools,
including on resume; the remaining declarations stay available for validation.

The consumer keeps generated payload and result codecs and `BindInput`
preparation. Native execution uses the existing durable child workflow path.
Accepted calls and retries validate their retained contract. New pages read
previous owner metadata and use the current registration only after current
input and paging checks. Direct static calls and dynamic registry Agent calls
retain their existing behavior. Update existing prior-reader callbacks to return
the exact `ResolvedToolset` they fetched along with their error. No regeneration or wire migration is
required to use this binding method with existing generated declarations.


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

### Upgrade independent Agent providers

Every native `AgentToolTarget` now requires `Workflow` and `Queue` in addition
to `Executor` and `Configuration`. Update the declarations at their producer;
these values must match the registered provider Agent route. Existing native
records without those fields must be replaced before consumers discover them.
Service tool registrations retain their current contract.

Remove consumer calls to `WithAgentExecutors`. Register each Agent definition
and its `AgentToolResolver` on the provider worker instead. Preparation uses
`runtime.prepare_agent_child.<agent-id>` on that worker's queue. Static child
preparation uses the parent's corresponding activity on the parent queue.
Finish accepted workflows on their old workers before replacing those workers;
old workflow histories refer to the earlier activity name. Do not mix the two
worker implementations on the same queue while such work is accepted.

New suspensions use `goa-ai.run-suspension.v14`. Older suspensions are rejected
without conversion. Hosts must finish them before upgrading or provide their
own recovery, such as a fresh turn using the retained conversation history.
The runtime does not delete history or choose that product recovery policy.
Rolling back requires finishing new-format work and restoring compatible
native declarations before the old workers are used again.

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
Schema descriptions containing `<`, `>`, or `&` survive native-search replay,
including after JSON persistence. Current and retained schemas use the same
JSON escaping; serialization alone does not count as a definition change.
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

Regenerate providers and consumers with the Goa revision pinned in this module.
Compare regenerated schema records and fingerprints against saved declarations;
the MCP upgrade also aligns shared schema metadata. Follow the
[declaration cutover requirements](runtime.md#executable-ownership-and-upgrade). Replace generated
startup `Discover` calls, `RegistryToolsets` inputs, and dynamic executor wiring
with `RegisterRegistry`. Publish generated `ToolSchemas()` records before
turning on dynamic consumers. Upgrade the registry to serve `ResolveToolset`
and `CallResolvedTool`; older registry servers cannot serve this consumer path.
Old registrations without `ConsumerContract` remain usable by their existing
static integrations but are rejected by dynamic consumers.

The saved consumer binding format also changes. `RegistryBinding.Resolution`
previously held protobuf JSON for `ResolveToolsetResponse`. It now holds generated
`RegistrySelection` JSON with exactly one `direct` or `registered` selection.
The direct variant contains the original resolved registration; the registered
variant additionally requires its `CatalogSource`. The runtime reads only the new
format.

Before starting new consumers, stop writers and convert every saved binding in
calls, results, run context and continuation records using the old protobuf
validator and the new generated `EncodeRegistrySelection` codec. Old bindings
become direct selections. Do not merely wrap the old JSON: the generated codec
uses its own field names. Preserve registry names, original tool names, complete
arguments, model arguments, call identities and pagination declarations. Retain
backups until replay and fresh-turn conversation recovery are verified. Old
readers cannot consume the new format; rollback requires restoring the preserved
old-format state and preventing new-format writes. Registration and provider
storage migrations remain separate.

The [registry storage upgrade](runtime.md#registry-storage-upgrade) is separate
from consumer loading and earlier wire-version migrations. It requires the
new Renew callback and an offline conversion with all old writers stopped.
The layout conversion preserves original fingerprints, saved calls, absolute
expiry, and permanent retirement history. The current release separately requires
wire protocol 13, regenerated declarations, and current provider admission. Do
not reset catalog data or reuse old version-derived tokens. The upgrade guide
lists source changes and the installation-owned conversion requirements; no
general-purpose converter is shipped.

Confirmation templates now read canonical JSON names: change `{{ .Key }}` to
`{{ .key }}` and use `{{ json .value }}` when inserting JSON values.
Use `index` for optional properties. This applies to static and dynamic tools;
it makes templates independent of a provider's generated Go type names.

Local tests cover SDK request/response encoding, streaming, history,
compaction, generated consumers, catalog changes, exact admission, and saved
execution contracts. Live model search quality and token savings require
deployment-specific evaluation; local tests do not establish those results.
