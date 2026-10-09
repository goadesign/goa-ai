# Repository guide

Read [AGENTS.md](AGENTS.md) for repository instructions and
[DESIGN.md](DESIGN.md) for architecture and ownership. Those files are the
maintained sources of contributor guidance.

Goa-AI includes agents, MCP clients and servers, and tool registries. MCP composes
with original Goa endpoints; ordinary HTTP and gRPC methods may coexist with MCP
methods in the same service. Generate contracts with the application's pinned
Goa version and never edit generated files directly.

Start with [the framework overview](docs/overview.md), then consult the
[DSL reference](docs/dsl.md) and [runtime reference](docs/runtime.md) for the
contract being changed. [The test guide](codegen/agent/tests/README.md) describes
focused generator checks. Follow the [release upgrade instructions](docs/releases/v0.88.0.md)
when changing deployed generated systems.
