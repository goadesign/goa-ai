# How Goa-AI fits together

Goa-AI extends Goa's design and code generation with AI agents, MCP clients and
servers, and distributed tool registries. One design describes the capabilities;
generated code supplies typed contracts; your application supplies behavior and
infrastructure.

Use the [README](../README.md) to choose a starting point and the
[quickstart](../quickstart/README.md) for a runnable project. This page explains the
components and their responsibilities. The [DSL reference](dsl.md) and
[runtime reference](runtime.md) own the detailed API contracts.

## Design, generation, and execution

```text
Goa design
    → service types, validation, and transport contracts
    → tool schemas, codecs, and registration
    → agent and completion clients
    → MCP clients and HTTP servers

Application behavior + generated contracts
    → agent runtime + execution engine + application-owned stores
```

The design is an ordinary Go package importing Goa and Goa-AI's DSL. `goa gen`
produces packages under `gen/` and an application-specific `AGENTS_QUICKSTART.md`.
Never edit generated contracts. `goa example` creates missing application files;
it does not overwrite existing implementations.

Run generation with the Goa version selected by the application module:

```sh
go run goa.design/goa/v3/cmd/goa gen <design-package-import-path>
```

## Services, tools, and agents

A Goa service owns its methods, types, authorization, and domain behavior.
A toolset groups capabilities an agent can call. An agent uses toolsets and
provides a planner that chooses the next action from the current conversation
and completed tool results.

Tools can use a generated `BindTo` executor for a service method, an
application-owned custom executor, a nested agent, an MCP client, or a registry
provider. The consuming agent sees generated tool definitions and validated
arguments regardless of the implementation.

`Args`, `Return`, and Goa types describe the model-visible contract. Generated
transforms connect it to the implementation's types. `Inject` supplies values
owned by execution rather than asking the model to invent them. Generated codecs
retain defaults, exact JSON names, constraints, and selected union branches.
Tagged, flat, and distinct-kind untagged unions follow their authored Goa mapping.

See [toolsets](dsl.md#toolset), [service bindings](dsl.md#bindto-service-method-binding),
[JSON codecs](json_codecs.md), and [payload defaults](tool_payload_defaults.md).

## The agent loop

1. The generated client submits an accepted run to the runtime and engine.
2. The planner receives the conversation and permitted tool definitions.
3. The runtime validates requested arguments and executes admitted calls.
4. Completed results return to the planner for another decision or final answer.
5. A request for human input saves unfinished state and ends that workflow;
   a trusted host can submit an answer to a continuing workflow.

Invalid tool arguments can receive field-specific correction and authored
examples within the agent's recovery budget. Business rules and authorization
remain with the service. A lost response from a side-effecting tool does not
establish that it is safe to execute again.

Policies restrict permitted tools, call and recovery budgets, and time. History
policies retain complete turns or request summaries; prompt caching and overrides
configure model inputs. Runtime code owns correlation, pagination continuation,
accepted tool identity, and valid execution transitions.

See [planners](runtime.md#planner-contract), [policies](runtime.md#policy-enforcement),
[history](runtime.md#history-policies), and [host continuations](runtime.md#external-input-and-workflow-continuations).

## Typed answers and nested agents

`Completion(...)` declares a direct structured assistant response. Generated
unary and streaming helpers validate it and return the authored Go type.
Forced or automatic typed tool output is a separate choice for responses that
should use the tool execution and correction path.

An agent can export tools for other agents. Calls create real child workflows;
parents receive typed results and linked progress while retaining cancellation
and continuation ownership.

See [direct completions](runtime.md#typed-direct-completions),
[typed tool output](runtime.md#forced-typed-tool-output), and
[agent composition](runtime.md#agent-as-tool-composition).

## MCP as another service interface

A service declares `MCP(...)`, a shared JSON-RPC POST route, and methods for its
MCP operations. The generator implements MCP 2026-07-28 through the original
configured Goa endpoints. Security, middleware, mapped URL inputs, inherited
contracts, and result views retain their ordinary meaning. HTTP and gRPC methods
can coexist with MCP methods.

Servers expose tools, resources, prompts, completions, paginated catalogs,
progress, subscriptions, additional input, and native durable Tasks. Generated
clients consume those contracts; shared runtime consumers support HTTP and stdio.
Generated stdio servers remain deferred. MCP clients and servers do not require
an agent runtime.

`InputExchange` and `TaskExchange` bind existing service operations. They do not
create an adapter-owned job store. Service code owns durable acceptance, job
state, access, and cancellation. Local executors and registry providers use the
same generated operations.

OAuth clients own discovery, grants, and renewal; the host supplies trusted
registration, consent, and private credential storage. Resource servers verify
configured signed tokens or authenticated introspection before domain execution.
Apps use ordinary HTML resources and caller visibility, with browser permissions
owned by the host. Skills use discovery and resource reads, manifest verification,
and host-owned loading and execution approval.

See [MCP integration](https://goa.design/docs/2-goa-ai/mcp-integration/),
[MCP declarations](dsl.md#mcp-server-definition), [authorization](runtime.md#mcp-callers),
[Apps](../integration_tests/apps/README.md), and [Skills](mcp_skills.md).

## Registries and tool search

Providers publish complete generated tool declarations to a registry. Consumers
can use a named toolset or discover its current catalog. The registry owns
admission, provider leases, call routing, and saved operation outcomes. Providers
own execution; consumers own their selected tool contracts and permissions.

Deferred search is independent of discovery. `Deferred()` loads a whole toolset
on demand; named selections defer only chosen compiled tools. A static catalog
can use search, and a registry catalog can be advertised immediately.

See [registry and search contracts](tool_search.md).

## Storage, models, and production

The in-memory engine and store support development within one process. Temporal
and an application-owned durable runtime store retain accepted work, continuations,
and cancellation across worker restarts. Configuring Temporal alone does not
make an in-memory store durable.

Applications construct model clients, stores, engines, and stream sinks at startup,
register dependencies, and seal the runtime before serving runs. Model adapters
support OpenAI, Anthropic, Bedrock, Vertex AI, and gateways with different provider
capabilities. Optional MongoDB and Redis/Pulse integrations supply particular
storage and streaming functions; they do not replace application authorization
or deployment ownership.

Large results retain explicit bounds and runtime-managed pagination. `ServerData`
keeps host data outside model requests. `NativeImage` retains image evidence
through a host reader that checks current access before supplying bytes.

See [production configuration](runtime.md#production-configuration),
[engines](runtime.md#available-engines), [model clients](runtime.md#model-clients),
[bounded results](runtime.md#bounded-results), and [native images](native_images.md).

## Evaluation and observation

Evaluation suites generate typed scenario hooks. Application hooks run the
product and supply tool calls, results, and final answers; exact checks and
calibrated model judging produce the report. Deterministic examples can run
without a live model, while live-model scenarios use their own infrastructure
and credentials.

A trusted host receives selected runtime stream events, including assistant text,
tool progress, child runs, and usage. It owns the public events it shows to users.
OpenTelemetry traces describe execution. The runtime store retains accepted
transcript evidence; product conversation storage remains application-owned.

See [evaluations](evals.md), [streaming](runtime.md#hooks-and-streaming), and
[telemetry](runtime.md#telemetry).

## Upgrade as one application release

Generated clients, servers, executors, runtime workers, and storage implementations
must share the current contract. Older MCP revisions, registry wire versions,
and executable suspension checkpoints are not interchangeable.

Read the [v0.88.0 release and upgrade instructions](releases/v0.88.0.md) before
replacing an existing deployment. The [architecture](../DESIGN.md) explains
ownership for contributors, and the [runtime reference](runtime.md) describes
storage, recovery, and provider-specific requirements.
