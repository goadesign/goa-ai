<p align="center">
  <a href="https://goa.design/docs/2-goa-ai/#gh-light-mode-only">
    <picture>
      <source media="(max-width: 600px)" srcset="docs/img/goa-ai-banner-mobile.png">
      <img alt="Goa-AI: one Go design generates typed agent tools, MCP clients and servers, and HTTP and gRPC APIs." src="docs/img/goa-ai-banner.png" width="960">
    </picture>
  </a>
  <a href="https://goa.design/docs/2-goa-ai/#gh-dark-mode-only">
    <picture>
      <source media="(max-width: 600px)" srcset="docs/img/goa-ai-banner-mobile-dark.png">
      <img alt="Goa-AI: one Go design generates typed agent tools, MCP clients and servers, and HTTP and gRPC APIs." src="docs/img/goa-ai-banner-dark.png" width="960">
    </picture>
  </a>
</p>

<p align="center">
  <a href="https://github.com/goadesign/goa-ai/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/goadesign/goa-ai"></a>
  <a href="https://github.com/goadesign/goa-ai/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/goadesign/goa-ai/ci.yml?branch=main"></a>
  <a href="https://pkg.go.dev/goa.design/goa-ai"><img alt="Go reference" src="https://pkg.go.dev/badge/goa.design/goa-ai.svg"></a>
  <a href="LICENSE"><img alt="MIT license" src="https://img.shields.io/badge/license-MIT-blue.svg"></a>
</p>

# Goa-AI: agents, MCP, and tool registries in Go

**Design your capabilities once. Generate the contracts that connect your services, AI agents, and MCP clients.**

Goa-AI extends [Goa](https://goa.design) with AI agents, the Model Context Protocol
(MCP), and distributed tool registries. Write a design in Go; generate typed
clients, servers, model schemas, validation, and runtime wiring. Implement your
application's behavior in ordinary Go.

For engineers, this means fewer contracts to maintain and compiler feedback when
they change. For coding agents, it means explicit types, generated validation,
and an application-specific guide to the code they need to implement.

**[Run the quickstart](#quick-start)** · **[Explore the guides](https://goa.design/docs/2-goa-ai/)** · **[Upgrade to v0.88.0](docs/releases/v0.88.0.md#required-upgrade-actions)**

## Choose what you want to build

| Your goal | Start here |
| --- | --- |
| **An AI agent** that calls typed tools, returns structured answers, and can pause for human input | [Agent quickstart](quickstart/README.md) |
| **An MCP server** exposing Go service methods, resources, and prompts, or **an MCP client** consuming them | [MCP integration guide](https://goa.design/docs/2-goa-ai/mcp-integration/) |
| **A tool registry** that publishes capabilities across services and lets agents discover them | [Registries and tool search](docs/tool_search.md) |

These capabilities compose. You can also use MCP servers and clients independently
of the agent runtime.

## One design, several ways to use it

A product lookup can be an HTTP endpoint, a gRPC method, an MCP tool, and a local
agent tool. The input, output, descriptions, and validation come from the same
design:

```go
package design

import (
    . "goa.design/goa/v3/dsl"
    . "goa.design/goa-ai/dsl"
)

var Lookup = Type("Lookup", func() {
    Field(1, "sku", String, "Product SKU", func() { MinLength(1) })
    Required("sku")
    Example(map[string]any{"sku": "SKU-123"})
})

var Product = Type("Product", func() {
    Field(1, "name", String, "Product name")
    Required("name")
})

var _ = Service("catalog", func() {
    Description("Look up products for applications and shopping assistants.")
    MCP("catalog", "1.0.0")
    JSONRPC(func() { POST("/mcp") })

    Method("lookup", func() {
        Description("Retrieve a product by its SKU.")
        Payload(Lookup)
        Result(Product)
        HTTP(func() { GET("/products/{sku}") })
        GRPC(func() {})
        Tool("lookup", "Look up a product by SKU")
    })

    Agent("shopper", "Help customers find products", func() {
        Use("products", func() {
            Tool("lookup", "Look up a product by SKU", func() {
                BindTo("lookup")
            })
        })
    })
})
```

Run generation using the Goa version selected by your application:

```bash
go run goa.design/goa/v3/cmd/goa gen <design-package-import-path>
```

| From this design | You get |
| --- | --- |
| Service contract | Typed Go interfaces, payloads, results, and validation |
| Agent tool | Model-facing JSON Schema, examples, codecs, and a service executor |
| MCP tool | A typed client and HTTP server with discovery and input/output schemas |
| HTTP and gRPC methods | Clients, servers, OpenAPI, and protobuf definitions |

Change a field and regenerate these contracts together. Your implementation,
planner, authorization, and business rules remain application code. Ordinary
methods can coexist with MCP tools in the same service.

Tools can expose a focused subset of a method's inputs and results, inject
server-owned values, or return a selected Goa result view. The generator handles
the declared transforms. See [service bindings](docs/dsl.md#bindto-service-method-binding)
and [MCP composition](docs/dsl.md#mcp-server-definition).

## Quick start

With **Go 1.27.0 or newer**, run the checked-in example:

```bash
git clone https://github.com/goadesign/goa-ai.git
cd goa-ai/quickstart
go run ./cmd/orchestrator
```

It runs the real agent loop and typed completions with a deterministic planner
and an in-memory engine and store. **No model API key, Temporal, Redis, or MongoDB
is required.** You'll see:

```text
Assistant: Tool helpers.answer returned {"text":"Tokyo is the capital of Japan."}
Completion draft_task: Prepare launch checklist (Confirm the service is ready to launch.), 3 steps
```

Run its evaluation suite:

```bash
go run ./cmd/chat_quality-evals
```

The [quickstart guide](quickstart/README.md) walks through the design, generation,
service wiring, and connecting a model. For MCP, follow the
[MCP integration guide](https://goa.design/docs/2-goa-ai/mcp-integration/).

## What you can build

### Agents and typed outputs

- **Typed tools and structured answers.** Bind tools to Goa methods or custom
  executors; declare `Completion(...)` for typed direct responses. Generated
  codecs and schemas enforce the authored contract.
- **Correction of invalid tool calls.** Missing fields, wrong types, and invalid
  values receive specific feedback and authored examples. The runtime can ask
  the model for a replacement within your recovery budget, before execution.
- **Specialist agents.** Expose agents as tools and compose real child runs with
  linked progress and cancellation.
- **Human input and approvals.** Save a pending question or confirmation and
  continue from the host's answer. Text-only runs exclude interactive tools.
- **Large results and images.** Bound model-visible results, continue paginated
  queries, retain native image evidence, and keep rich host data outside model
  requests.

[Agent DSL](docs/dsl.md#agent-use-and-export) · [Runtime and composition](docs/runtime.md) · [Native images](docs/native_images.md)

### MCP clients and servers

- **MCP 2026-07-28 over HTTP.** Generate stateless servers and typed clients with
  discovery, tools, structured results, and ordered text, image, audio, resource
  links, and embedded resources. Consumers also support stdio; generated stdio
  servers are deferred.
- **Resources and prompts.** Serve fixed or parameterized text/binary resources,
  typed prompts, argument completion, and authenticated paginated catalogs.
- **Progress and subscriptions.** Report progress from ordinary service methods;
  observe catalog changes, resource updates, and full Task state.
- **Additional input and durable Tasks.** Collect form input or URL consent;
  expose existing durable jobs with creation, observation, answers, and
  cancellation. The same native methods work as local and registry tools.
- **Authorization.** Compose browser OAuth, machine credentials, signed client
  authentication, and enterprise identity exchange. Servers verify configured
  signed tokens or use token introspection. Hosts own trusted registration,
  consent, and private credential storage.
- **Apps and Skills.** Associate tools with HTML app resources and caller
  visibility; publish Skill catalogs and files with manifest verification.
  Reference hosts demonstrate browser isolation and content-bound approval.

[MCP declarations](docs/dsl.md#mcp-server-definition) · [Clients and authorization](docs/runtime.md#mcp-callers) · [Apps example](integration_tests/apps/README.md) · [Skills guide](docs/mcp_skills.md)

### Registries, search, and production execution

- **Distributed tool registries.** Publish typed toolsets, discover changing
  catalogs, and route calls to providers with exact contracts and renewable leases.
- **Load tools when needed.** Use deferred tool search for whole toolsets or
  selected compiled tools, through supported OpenAI and Claude integrations.
- **Durable execution.** Use Temporal and an application-owned durable store to
  retain accepted work, host continuations, remote Task identity, and cancellation
  across worker restarts. The in-memory engine supports local development.
- **Policies and context.** Control permitted tools, call and recovery budgets,
  timing, history compression, prompt caching, and prompt overrides.
- **Evaluation and observability.** Generate typed evaluation suites; observe
  assistant text, tool progress, child runs, usage, and OpenTelemetry traces.
- **Standalone JSON codecs.** Generate strict encoders and decoders beside
  ordinary Goa types, independently of agent and MCP declarations.

[Registries and search](docs/tool_search.md) · [Production configuration](docs/runtime.md#production-configuration) · [Evaluations](docs/evals.md) · [JSON codecs](docs/json_codecs.md)

## Build with a coding agent

Install the **Goa service designer skill** in your application project
(requires Node.js and npm):

```bash
npx skills add goadesign/goa --skill goa-service-designer
```

Give your coding agent the generated `AGENTS_QUICKSTART.md`. It names **your**
agents, tools, packages, and registration functions. A useful first task:

> Add a product lookup tool backed by the catalog service. Update the Goa design,
> regenerate, implement the service method, and verify valid and invalid calls.
> Use AGENTS_QUICKSTART.md for wiring. Do not edit gen/.

`goa gen` replaces generated contracts. `goa example` creates missing application
scaffolding without overwriting existing files. Your application behavior stays
yours. See the [coding-agent workflow](https://goa.design/docs/ai-development/).

## Take it to production

Choose model adapters for OpenAI, Anthropic, Amazon Bedrock, Google Vertex AI,
or a model gateway. [Provider support](docs/runtime.md#model-clients) varies by
model and API. Optional integrations include MongoDB for memory and prompt
overrides, and Redis/Pulse for streams and registries.

Your application owns planners, service behavior, authorization, side-effect
idempotency, durable storage, and deployment. Configure the Temporal engine
**and** a durable runtime store for restart-safe execution. A trusted host
receives runtime events and decides what to expose to users.

**v0.88.0 is a breaking upgrade.** Regenerate and coordinate servers, clients,
providers, runtime workers, and storage implementations. Older MCP revisions,
registry wire versions, and suspension checkpoints are not interchangeable.
Read the [release and upgrade instructions](docs/releases/v0.88.0.md) before
replacing existing deployments; a source rollback alone cannot undo new stored
work.

## Learn more

- [Guided documentation](https://goa.design/docs/2-goa-ai/) — start with agents, MCP, or registries.
- [Framework overview](docs/overview.md) — how design, generation, runtime, and engines fit together.
- [DSL reference](docs/dsl.md) and [runtime reference](docs/runtime.md) — exact contracts and supported choices.
- [Architecture](DESIGN.md) — ownership and execution flows for contributors.
- [Go package reference](https://pkg.go.dev/goa.design/goa-ai) and [release notes](https://github.com/goadesign/goa-ai/releases).

## Contributing

Issues and PRs are welcome. Include a Goa design, failing test, or clear
reproduction when reporting behavior. See [AGENTS.md](AGENTS.md) for repository
guidelines.

Run `make setup` once after cloning or when `.tool-versions` or `.go-install`
changes. It installs the exact protobuf compiler and Go generators used by CI.
Normal `make` targets verify those versions before building or generating code.

## License

MIT License (C) Raphael Simon and the [Goa community](https://goa.design).
