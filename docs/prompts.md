# Composable Prompt Management in Goa-AI

Goa-AI provides first-class, composable prompt declarations, compile-time template validation, typed code generation, runtime scoped overrides, and per-request OpenTelemetry tracing.

Prompts in Goa-AI are not hardcoded strings scattered across planner code, nor are they opaque runtime configuration blobs. You declare baseline prompts directly in Goa service and agent designs. `goa gen` parses the template Abstract Syntax Trees (ASTs), validates field references against typed argument schemas, and emits typed render helpers alongside canonical JSON schema catalogs.

At runtime, an immutable baseline prompt registry resolves templates and integrates with a scoped store for tenant-, environment-, or session-level overrides. When prompts render, Goa-AI tracks the exact root and composed sub-prompt versions, returning `PromptContent.Refs` which callers pass on `model.Request.PromptRefs` so the model client records prompt provenance on OpenTelemetry spans.

---

## Architecture and Lifecycle

Composable prompt management follows a clear four-phase lifecycle:

```text
1. Goa Design
   ├── SystemPrompt(...)           (Agent baseline instructions)
   ├── Agent Prompt(...)           (Agent-scoped prompts: <service>.<agent>.<name>)
   └── Service Prompt(...)         (Service-shared prompts: <service>.<name>)
          │
          ▼
2. Code Generation (`goa gen`)
   ├── Static AST validation      (text/template missingkey=error against Args struct)
   ├── Typed Go code              (gen/<svc>/agents/<agent>/prompts.go, gen/<svc>/prompts/)
   └── Schema catalogs            (specs/prompt_schemas.json)
          │
          ▼
3. Startup and Registration
   ├── Agent registration         (Register<Agent>Agent preflights & installs baseline specs)
   ├── Service prompt registration(prompts.RegisterPrompts(rt.PromptRegistry))
   └── Override store attachment  (runtime.WithPromptStore(store))
          │
          ▼
4. Execution, Provenance, and Observability
   ├── Typed rendering            (RenderSystemPrompt, Render<Name>Prompt)
   ├── Sub-prompt composition     ({{ render "<prompt_id>" [data] }} with runtime cycle detection)
   ├── Per-request provenance     (model.Request.PromptRefs from PromptContent.Refs)
   └── OpenTelemetry spans        (gen_ai.prompt.ids, gen_ai.prompt.versions, goa_ai.prompt.refs)
```

### Core Terminology

- **PromptSpec**: The immutable baseline specification for a prompt template. It records the prompt's canonical identifier (`Ident`), owning agent (if agent-scoped), role (`system`, `user`, `tool`, `synthesis`), description, template source, deterministic SHA-256 version, optional `Funcs` map, and typed input reflection contract (`ArgsType`).
- **PromptRef**: A lightweight identity pair `(ID, Version)` attached to rendered outputs and model requests.
- **PromptContent**: The result of rendering a prompt. It contains the rendered string `Text`, the root prompt's `Ref`, and an ordered slice `Refs` containing the root prompt and every composed sub-prompt in composition encounter order.
- **Scope**: A runtime targeting struct containing an optional `SessionID` and an arbitrary key/value map of `Labels` (for example, `tenant`, `environment`, `region`) used to select active overrides.
- **Override**: A stored prompt template that replaces a baseline prompt for a matching `Scope`.

---

## Declaring Prompts in Goa Design

Goa-AI provides two primary DSL functions for prompt declarations: `SystemPrompt` and `Prompt`.

### Agent System Prompts (`SystemPrompt`)

Every agent can declare its baseline system instructions using `SystemPrompt` inside the `Agent` declaration. `SystemPrompt` assigns the canonical prompt identifier `<service>.<agent>.system` and sets the role to `prompt.PromptRoleSystem` (`"system"`).

`SystemPrompt` supports three syntax variants:

#### 1. Static Text Shorthand

For agents whose system prompt does not require dynamic arguments:

```go
package design

import (
    . "goa.design/goa/v3/dsl"
    . "goa.design/goa-ai/dsl"
)

var _ = Service("support", func() {
    Agent("assistant", "Customer support assistant", func() {
        SystemPrompt("You are a helpful customer support assistant for Acme Corp. Always remain polite and concise.")
    })
})
```

#### 2. Parameterized Template with Closure and Typed Args

When the system prompt accepts structured inputs (such as tenant names, user preferences, or runtime style guidelines), supply the template string followed by a DSL closure defining `Args`:

```go
var _ = Service("support", func() {
    Agent("assistant", "Customer support assistant", func() {
        SystemPrompt("You are {{ .AssistantName }}. Follow these guidelines: {{ .StyleGuide }}", func() {
            Description("Baseline customer support assistant instructions parameterized by tenant")
            Args(func() {
                Attribute("assistant_name", String, "Display name of the assistant")
                Attribute("style_guide", String, "Tone, style, and escalation guidelines")
                Required("assistant_name", "style_guide")
            })
        })
    })
})
```

#### 3. Full DSL Closure

You can also specify the template inside the closure using `Template(...)`:

```go
var _ = Service("support", func() {
    Agent("assistant", "Customer support assistant", func() {
        SystemPrompt(func() {
            Description("Baseline customer support assistant instructions")
            Template("You are an AI assistant helping customers resolve billing issues.")
        })
    })
})
```

### Agent-Scoped Prompts (`Prompt` inside `Agent`)

To declare user prompts, few-shot prompt templates, tool instructions, or synthesis prompts owned by an agent, use `Prompt` inside an `Agent` declaration. The canonical prompt identifier is `<service>.<agent>.<name>`, and the default role is `prompt.PromptRoleUser` (`"user"`).

```go
var Ticket = Type("Ticket", func() {
    Attribute("id", String, "Ticket ID")
    Attribute("customer_tier", String, "Customer service tier (standard, premium, enterprise)")
    Attribute("issue_description", String, "Customer inquiry text")
    Required("id", "customer_tier", "issue_description")
})

var _ = Service("support", func() {
    Agent("assistant", "Customer support assistant", func() {
        SystemPrompt("You are {{ .AssistantName }}.", func() {
            Args(func() {
                Attribute("assistant_name", String, "Assistant name")
                Required("assistant_name")
            })
        })

        Prompt("triage_ticket", func() {
            Description("User prompt template for incoming customer support inquiries")
            Role("user")
            Template(`Please analyze ticket {{ .Ticket.ID }} (Tier: {{ .Ticket.CustomerTier }}):
{{ .Ticket.IssueDescription }}

{{ render "support.shared_safety" . }}`)
            Args(func() {
                Attribute("ticket", Ticket, "Incoming ticket details")
                Required("ticket")
            })
        })
    })
})
```

Inside an agent `Prompt` closure:
- `Role(role)`: Sets the prompt role (`"system"`, `"user"`, `"tool"`, or `"synthesis"`).
- `Template(source)`: Defines the Go `text/template` template string.
- `Description(desc)`: Documents the prompt's purpose in generated code and schema catalogs.
- `Args(dsl)`: Defines the structured input schema available to the template.

### Service-Shared Prompts (`Prompt` inside `Service`)

When a prompt template is shared across multiple agents, used in service-owned direct completions, or evaluated in non-agent service operations, declare it directly inside `Service(...)`. The canonical prompt identifier is `<service>.<name>`.

Service-level prompts require a DSL closure containing `Template(...)`:

```go
var _ = Service("support", func() {
    Prompt("shared_safety", func() {
        Description("Shared safety and compliance instructions across all support agents")
        Role("system")
        Template("Never disclose internal API keys, database credentials, or personally identifiable customer data.")
    })

    Prompt("summarize_transcript", func() {
        Description("Prompt for generating a concise customer support ticket summary")
        Role("user")
        Template("Summarize the following customer transcript in {{ .MaxSentences }} sentences: {{ .Transcript }}")
        Args(func() {
            Attribute("transcript", String, "Full conversation transcript")
            Attribute("max_sentences", Int, "Maximum sentence count")
            Required("transcript", "max_sentences")
        })
    })
})
```

### Distinguishing Internal Prompts from MCP Client-Facing Prompts

Goa-AI also supports the Model Context Protocol (MCP). It is important to distinguish internal composable prompts from MCP prompts:

| Capability | Context | Canonical ID | Target Consumer | Purpose |
| :--- | :--- | :--- | :--- | :--- |
| **Agent System Prompt** | `Agent(...)` | `<svc>.<agent>.system` | LLM / Planner | Base system instructions for an agent |
| **Agent Prompt** | `Agent(...)` | `<svc>.<agent>.<name>` | LLM / Planner | Turn templates, few-shot examples, triage prompts |
| **Service Prompt** | `Service(...)` | `<svc>.<name>` | Multi-agent / Completions | Reusable shared templates across services |
| **MCP Method Prompt** | `Method(...)` (in MCP service) | Authored prompt name | External MCP Clients | Protocol messages returned via MCP `prompts/get` |
| **Static MCP Prompt** | `Service(...)` (with MCP) | Authored prompt name | External MCP Clients | Static MCP prompt catalog returned via `prompts/list` |

An MCP method prompt declared with `Prompt(name, description)` inside a `Method(...)` exports a capability to external MCP clients over JSON-RPC using the authored prompt `name` (which may differ from the owning Goa method name, for example `Method("build", func() { Prompt("simple", "...") })`). In contrast, `SystemPrompt` and `Prompt` on `Agent` and `Service` configure internal, typed prompt execution for agents and services.

---

## Sub-Prompt Composition

Prompt templates can compose other registered prompts using the built-in `render` template function:

```gotemplate
{{ render "<prompt_id>" [data] }}
```

### Data Passing Semantics

1. **Implicit Data Forwarding**: When called with one argument, `render` forwards the parent template's **root input data**:
   ```gotemplate
   {{ render "support.shared_safety" }}
   ```
2. **Explicit Data Argument**: When called with two arguments, `render` evaluates and passes that explicit value to the sub-prompt. This allows passing sub-fields or passing the current cursor when `.` is rebound inside a `range` or `with` block:
   ```gotemplate
   {{ render "support.customer_profile" .Customer }}
   ```

### Provenance Tracking across Composition Chains

When a template renders one or more sub-prompts via `render`:
- The root prompt and all sub-prompts are resolved against the active `Scope`.
- `PromptContent.Ref` points to the root prompt's `PromptRef` `(ID, Version)`.
- `PromptContent.Refs` contains the root prompt followed by every rendered sub-prompt in **composition encounter order**, deduplicated by exact `(ID, Version)` pair:
  ```go
  // If agent.system renders support.shared_safety and support.formatting:
  content.Refs == []prompt.PromptRef{
      {ID: "support.assistant.system", Version: "a1b2c3d4..."},
      {ID: "support.shared_safety", Version: "e5f6g7h8..."},
      {ID: "support.formatting", Version: "9i0j1k2l..."},
  }
  ```

### Cycle Detection

`goa gen` parses prompt templates using a stub `render` function so field references can be validated against declared `Args` without requiring sub-prompts to be evaluated during code generation.

Recursive composition cycles (for example, prompt `A` rendering prompt `B`, which renders prompt `A`) are detected and rejected at runtime during `Registry.Render`. The registry maintains a render traversal stack in the context. If a baseline prompt or dynamic override introduces a composition cycle, `Registry.Render` halts immediately and returns `prompt.ErrTemplateCycle` with the full chain of prompt IDs.

---

## Compile-Time Validation and Generated Code

When you run `goa gen`, Goa-AI validates every prompt declaration before emitting Go code:

### Compile-Time vs. Runtime AST Validation

Template AST validation runs in two complementary phases with distinct type sources:

1. **Compile-Time Validation (`goa gen` over the Goa `Args` Graph)**:
   During code generation, `goa gen` parses every declared `Template` with Go's `text/template` (`missingkey=error`) and traverses the `text/template/parse` AST across direct dot accesses (`{{ .FieldName }}`), pipeline commands and variable bindings (`{{ $item := .Items }}`), nested scopes (`with`, `range`, `if`), and root references (`{{ $.FieldName }}`). It validates these field paths against the **Goa `Args` attribute graph** (`*goaexpr.AttributeExpr`, accounting for Goa's `Goify` rules and any custom `struct:field:name` metadata). If a template references a field not declared in the Goa `Args` graph, or if `Args` is omitted while fields are accessed, `goa gen` fails fast with an error naming the file, prompt, and missing field. Because `goa gen` inspects the Goa design graph before external Go packages are compiled, an attribute whose type is replaced via `Meta("struct:field:type", "extpkg.CustomConfig", "example.com/extpkg")` has opaque inner fields and methods at `goa gen` time; sub-paths beneath that external field are treated as dynamic during code generation and verified at runtime against the compiled Go type.

2. **Runtime Validation (`PromptSpec.ArgsType` Reflection at Registration, Override, and Render)**:
   Generated prompt specifications bind `spec.ArgsType` (`reflect.Type`) to the compiled Go argument struct (`reflect.TypeFor[<Name>PromptArgs]()` when `Args(...)` is declared, or `reflect.TypeFor[struct{}]()` for generated zero-argument prompts). At runtime—during `Register` / `ValidateSpecs`, `ValidateOverride` / `SetOverride`, and at the selected-template `Render` parse boundary before execution—the registry validates the template AST against the compiled `spec.ArgsType` and `spec.Funcs`. Because runtime validation reflects over the real compiled Go struct, it enforces:
   - Rejecting `.Field` references on generated zero-argument prompts (`ArgsType == reflect.TypeFor[struct{}]()`).
   - Verifying fields and Go methods on custom external `struct:field:type` fields (for example, rejecting `{{ .CustomConfig.MissingField }}` or an unexported/non-existent method on `extpkg.CustomConfig` when `Register` or `Render` runs).
   - Manually registered `PromptSpec` values with `ArgsType == nil` accept untyped/dynamic data (such as JSON-decoded maps) and validate template syntax and declared `Funcs` only.

### Generated Agent Artifacts

For each agent declaring prompts, `goa gen` emits `gen/<service>/agents/<agent>/prompts.go` and `gen/<service>/agents/<agent>/specs/prompt_schemas.json`.

The emitted `prompts.go` file contains the following exported symbols:

| Emitted Symbol | Role / Type Signature | Description |
| :--- | :--- | :--- |
| `SystemPromptID` | `prompt.Ident = "<service>.<agent>.system"` | Canonical identifier for the agent's system prompt (when `SystemPrompt` is declared). |
| `Prompt<Name>ID` | `prompt.Ident = "<service>.<agent>.<name>"` | Canonical identifier for a named agent prompt. |
| `SystemPromptArgs` | `struct { ... }` | Typed arguments struct for `SystemPrompt` (emitted only when `Args(...)` is declared). |
| `<Name>PromptArgs` | `struct { ... }` | Typed arguments struct for a named prompt (emitted only when `Args(...)` is declared). |
| `PromptSpecSystem()` | `func() prompt.PromptSpec` | Precomputed baseline `PromptSpec` factory for the system prompt. |
| `PromptSpec<Name>()` | `func() prompt.PromptSpec` | Precomputed baseline `PromptSpec` factory for a named prompt. |
| `PromptSpecs()` | `func() []prompt.PromptSpec` | Aggregator returning all baseline prompt specs declared for this agent. |
| `RegisterPrompts` | `func(reg *prompt.Registry) error` | Helper registering all agent prompt specs into a registry. |
| `RenderSystemPrompt` | `func(ctx, pctx[, args]) (*prompt.PromptContent, error)` | Resolves and renders the system prompt using run-scoped `PlannerContext`. Takes `args` only when `Args(...)` is declared. |
| `Render<Name>Prompt` | `func(ctx, pctx[, args]) (*prompt.PromptContent, error)` | Resolves and renders a named prompt using `PlannerContext`. Takes `args` only when `Args(...)` is declared. |
| `RenderSystemPromptWithRegistry` | `func(ctx, reg, scope[, args]) (*prompt.PromptContent, error)` | Renders the system prompt directly against `*prompt.Registry` and `prompt.Scope`. |
| `Render<Name>PromptWithRegistry` | `func(ctx, reg, scope[, args]) (*prompt.PromptContent, error)` | Renders a named prompt directly against `*prompt.Registry` and `prompt.Scope`. |

### Constructor Purity and Registration Sequence

Registration follows strict constructor purity and an explicit four-step startup sequence:
1. **Pure constructor**: `New<Agent>Registration(rt, cfg)` packages the agent's definition and attaches `PromptSpecs: PromptSpecs()` to `AgentRegistration` without causing side effects or mutating registries.
2. **Preflight validation**: When the application calls `rt.RegisterAgent(ctx, reg)` (or the convenience wrapper `Register<Agent>Agent(ctx, rt, cfg)`), `RegisterAgent` first validates the agent configuration, tool specs, and all `reg.PromptSpecs` via `rt.PromptRegistry.ValidateSpecs(reg.PromptSpecs)`—checking non-empty IDs/templates, template syntax, `ArgsType` AST references, intra-batch duplicates, and collisions with already-registered prompt IDs (`prompt.ErrDuplicatePromptSpec`) before calling the workflow engine.
3. **Engine registration**: `RegisterAgent` then registers the runtime storage, operation, and continuation activities, the agent workflow and cancellation workflow, planner activities, and `ExecuteToolActivity` with `rt.Engine`.
4. **Prompt installation and agent publication**: After engine registration calls succeed, `RegisterAgent` loops over `reg.PromptSpecs` calling `rt.PromptRegistry.Register(spec)` for each spec, and then publishes the `AgentRegistration` and executable tool specs into the runtime's agent maps. (`RegisterAgent` executes these steps sequentially in-process without a cross-engine rollback or global transaction.)

### Generated Service-Level Prompts

For service-shared prompts, `goa gen` emits `gen/<service>/prompts/prompts.go` containing:
- Canonical constants: `Prompt<Name>ID` (with value `<service>.<name>`)
- Typed argument structs: `<Name>PromptArgs`
- Spec factories: `PromptSpec<Name>()` (with `AgentID: ""`)
- Package aggregator: `PromptSpecs() []prompt.PromptSpec`
- Registration helper: `RegisterPrompts(reg *prompt.Registry) error`
- Typed renderers: `Render<Name>Prompt(...)` and `Render<Name>PromptWithRegistry(...)`

Applications register service-level prompts once at startup:

```go
import "myproject/gen/support/prompts"

if err := prompts.RegisterPrompts(rt.PromptRegistry); err != nil {
    log.Fatalf("failed to register service prompts: %v", err)
}
```

---

## Rendering Prompts in Application Code

### In Planners (`PlanStart` and `PlanResume`)

Inside an agent planner, render prompts using the generated typed helpers, passing `in.Agent` (which implements `planner.PlannerContext`). Then, copy the returned `content.Refs` to `model.Request.PromptRefs`:

```go
package planner

import (
    "context"
    "errors"
    "fmt"

    genagent "myproject/gen/support/agents/assistant"
    "goa.design/goa-ai/runtime/agent/model"
    "goa.design/goa-ai/runtime/agent/planner"
)

type SupportPlanner struct{}

func (p *SupportPlanner) PlanStart(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
    // 1. Render the system prompt using the generated helper and typed arguments.
    systemContent, err := genagent.RenderSystemPrompt(ctx, in.Agent, genagent.SystemPromptArgs{
        AssistantName: "Acme Support Bot",
        StyleGuide:    "Empathetic, clear, and action-oriented.",
    })
    if err != nil {
        return nil, fmt.Errorf("render system prompt: %w", err)
    }

    // 2. Obtain the model client for this turn.
    mc, ok := in.Agent.PlannerModelClient("anthropic")
    if !ok {
        return nil, errors.New("model client anthropic is not configured")
    }

    // 3. Assemble messages, prepending the rendered system prompt.
    messages := []*model.Message{{
        Role:  model.ConversationRoleSystem,
        Parts: []model.Part{model.TextPart{Text: systemContent.Text}},
    }}
    messages = append(messages, in.Messages...)

    // 4. Stream or complete with advertised tools and prompt provenance attached.
    sum, err := mc.Stream(ctx, &model.Request{
        Messages:   messages,
        Tools:      in.Agent.AdvertisedToolDefinitions(),
        PromptRefs: systemContent.Refs, // Root and composed sub-prompt versions for tracing
        Stream:     true,
    })
    if err != nil {
        return nil, fmt.Errorf("model stream: %w", err)
    }

    // 5. Return tool calls or final assistant response.
    if len(sum.ToolCalls) > 0 {
        return &planner.PlanResult{ToolCalls: sum.ToolCalls}, nil
    }
    return &planner.PlanResult{FinalResponse: sum.FinalResponse()}, nil
}
```

When using unary `mc.Complete(ctx, req)`, assistant messages are returned in `resp.Content` (`[]model.Message`), never on a single response field.

### The Per-Request Provenance Contract

A planner may issue several model calls in a single turn (for example, preliminary classification, search re-ranking, and final reasoning), each using different prompts.

To prevent cross-request contamination:
- `PlannerContext.RenderPrompt` **does not** stage mutable `PromptRef`s on the planner context.
- The caller explicitly passes `PromptRefs: content.Refs` on the exact `model.Request` whose messages contain `content.Text`.
- `PlannerContext.RenderPrompt` records turn-wide `PromptRendered` hook events via an internal `prompt.RenderRecorder` **atomically only after the top-level `Render` succeeds**. If a template or any sub-prompt in the composition tree fails to render, no render events are published.

### Ordering Distinction: Request Provenance vs. Event Audit

There is an intentional ordering distinction between request provenance, recorded audit events, and run record resolution:

1. **`PromptContent.Refs`**: Preserves **composition encounter order** (`[rootPrompt, ...childPrompts]`), reflecting the structural hierarchy of how the prompt was assembled for the model.
2. **`RenderRecorder.Events()`**: Sorted **canonically by `(PromptID, Version, SessionID, Scope)`**. Canonical sorting guarantees that concurrent sub-prompt evaluation or parallel tool rendering cannot alter workflow start identity or create non-deterministic run replay hashes.
3. **`Runtime.ResolvePromptRefs(ctx, sessionID, runID)`**: Returns unique `PromptRef`s in **run-log and parent/child run traversal order**, reconstructing prompt history breadth-first across the execution graph without sorting by prompt ID.

### Rendering Outside Planners (Pre-Start Rendering)

When client code renders a prompt before initiating a workflow (for example, rendering an initial user message to display in a client UI or pass to `client.Start`), use `Render<Name>PromptWithRegistry`:

```go
recorder := prompt.NewRenderRecorder()
renderCtx := prompt.WithRenderRecorder(ctx, recorder)

scope := prompt.Scope{
    SessionID: sessionID,
    Labels: map[string]string{
        "tenant_id": "tenant-123",
    },
}

content, err := genagent.RenderTriageTicketPromptWithRegistry(renderCtx, rt.PromptRegistry, scope, genagent.TriageTicketPromptArgs{
    Ticket: ticket,
})
if err != nil {
    return err
}

messages := []*model.Message{
    {
        Role:  model.ConversationRoleUser,
        Parts: []model.Part{model.TextPart{Text: content.Text}},
    },
}

// Pass recorded events to Start so workflow history preserves prompt provenance:
if _, err := client.Start(ctx, sessionID, messages,
    runtime.WithRunID(runID),
    runtime.WithRenderedPrompts(recorder.Events()),
); err != nil {
    return err
}
```

---

## Runtime Overrides and Scopes

Goa-AI enables prompt iteration and dynamic A/B testing without redeploying code or recompiling designs.

### Configuring Override Storage

Attach a `prompt.Store` to the runtime using `runtime.WithPromptStore`:

```go
import (
    "goa.design/goa-ai/runtime/agent/prompt"
    "goa.design/goa-ai/runtime/agent/runtime"
    promptmongo "goa.design/goa-ai/features/prompt/mongo"
    clientmongo "goa.design/goa-ai/features/prompt/mongo/clients/mongo"
)

// In-Memory store for tests and local development:
inmemStore := prompt.NewInMemoryStore()
rt := runtime.New(storageStore, runtime.WithPromptStore(inmemStore))

// Production MongoDB store:
mongoClient, err := clientmongo.New(clientmongo.Options{
    Client:     rawMongoClient,
    Database:   "assistant_runtime",
    Collection: "prompt_overrides",
})
if err != nil {
    return fmt.Errorf("create prompt mongo client: %w", err)
}
mongoStore, err := promptmongo.NewStore(mongoClient)
if err != nil {
    return fmt.Errorf("create prompt mongo store: %w", err)
}
rtProd := runtime.New(storageStore, runtime.WithPromptStore(mongoStore))
```

### Managing Overrides with `rt.PromptRegistry`

`rt.PromptRegistry` exposes five lifecycle methods for prompt overrides:

```go
// 1. Validate an override template against the baseline spec's ArgsType:
if err := rt.PromptRegistry.ValidateOverride("support.assistant.system", "You are {{ .AssistantName }}. Be brief."); err != nil {
    return err
}

// 2. Set an override targeted to a specific scope:
scope := prompt.Scope{
    Labels: map[string]string{"tier": "enterprise"},
}
metadata := map[string]string{"author": "alice@acme.com", "experiment": "v2-concise"}
if err := rt.PromptRegistry.SetOverride(ctx, "support.assistant.system", scope, "You are {{ .AssistantName }}. Prioritize enterprise SLA.", metadata); err != nil {
    return err
}

// 3. Resolve the active prompt spec and highest-precedence override:
spec, activeOverride, err := rt.PromptRegistry.Resolve(ctx, "support.assistant.system", scope)
if err != nil {
    return err
}
if activeOverride != nil {
    fmt.Printf("Prompt %s active override version: %s\n", spec.ID, activeOverride.Version)
}

// 4. Inspect revision history for a prompt:
history, err := rt.PromptRegistry.History(ctx, "support.assistant.system")
if err != nil {
    return err
}
for _, record := range history {
    fmt.Printf("Version: %s, Created: %s, Metadata: %v\n", record.Version, record.CreatedAt, record.Metadata)
}

// 5. Destructively reset an override for an exact scope:
if err := rt.PromptRegistry.ResetOverride(ctx, "support.assistant.system", scope); err != nil {
    return err
}
```

When an application exposes override management through its own Goa service endpoints (for example, an operator admin API), the Goa service owns caller authentication, authorization for the target `promptID` and `scope` (including whether global overrides with an empty `Scope` are permitted), payload validation, and mapping `prompt.Err*` errors (`prompt.ErrPromptNotFound`, `prompt.ErrPromptStoreNotConfigured`, `prompt.ErrInvalidPromptSpec`, `prompt.ErrTemplateParse`, `prompt.ErrTemplateFieldNotFound`, `prompt.ErrTemplateCycle`) to transport errors. `rt.PromptRegistry` is an in-process runtime component and does not enforce caller access control.

### Scope Matching and Precedence Rules

When `Resolve` evaluates which template to render, it matches stored overrides against the requested `prompt.Scope`.

1. **Filtering (`ScopeMatches`)**:
   - An override applies when its `SessionID` (if non-empty) equals the request's `SessionID`, and every label key in the override exists on the request with the same value.
2. **Precedence Ranking (`OverridePrecedes`)**:
   When multiple overrides apply, Goa-AI selects the winner using three precedence tiers:
   - **Session Presence**: An override with a `SessionID` beats one without.
   - **Label Specificity**: Otherwise, the override with more labels wins.
   - **Recency**: Otherwise, the newer override (`CreatedAt`) wins.
3. **Fallback to Baseline**:
   If no override matches the requested scope, `Resolve` returns `(spec, nil, nil)`, and the registry renders the immutable baseline template declared in the Goa design.

### The Exact-Scope Destructive `Reset` Contract

`ResetOverride(ctx, id, scope)` (and `Store.Reset(ctx, id, scope)`) permanently removes all stored override records matching the **exact** `(promptID, scope)` pair:
- Matching on scope is exact: `SessionID` must match exactly, and the `Labels` map must contain identical key/value entries with no extra or missing keys (including empty values, dotted keys, or `$`-prefixed keys).
- It does **not** clear broader or narrower overrides. For example, resetting an override scoped to `{tier: "enterprise", region: "us-west"}` leaves any `{tier: "enterprise"}` override untouched.
- After `ResetOverride`, subsequent calls to `Resolve` fall back to the next applicable less-specific override or the baseline `PromptSpec`.
- `History` returns only the remaining stored records.

---

## OpenTelemetry Model Span Attributes

When a planner or application passes `model.Request.PromptRefs`, Goa-AI's model client automatically records prompt provenance on the `gen_ai.chat` client span:

```text
Span: gen_ai.chat
  Attributes:
    gen_ai.system:               "anthropic"
    gen_ai.request.model:        "claude-3-7-sonnet"
    gen_ai.prompt.ids:           ["support.assistant.system", "support.shared_safety"]
    gen_ai.prompt.versions:      ["7f9a1c2d3e4f...", "1a2b3c4d5e6f..."]
    goa_ai.prompt.refs:          ["support.assistant.system@7f9a1c2d3e4f...", "support.shared_safety@1a2b3c4d5e6f..."]
```

These span attributes link every model invocation directly to the exact prompt versions that produced it, enabling prompt regression analysis, cost attribution by prompt template, and telemetry correlation across production deployments.

---

## External `prompt.Store` Implementer Migration Guide

If your application implements a custom `prompt.Store` (for example, on PostgreSQL, DynamoDB, or Redis), update your type to satisfy the expanded `prompt.Store` interface:

```go
type Store interface {
    Resolve(ctx context.Context, promptID Ident, scope Scope) (*Override, error)
    Set(ctx context.Context, promptID Ident, scope Scope, template string, metadata map[string]string) error
    Reset(ctx context.Context, promptID Ident, scope Scope) error
    History(ctx context.Context, promptID Ident) ([]*Override, error)
    List(ctx context.Context) ([]*Override, error)
}
```

### Implementing the `Reset` Contract

Your `Reset` implementation must adhere to these invariants:
1. **Exact Scope Matching**: Permanently delete records whose `PromptID` matches `promptID`, `Scope.SessionID` matches `scope.SessionID`, and `Scope.Labels` matches `scope.Labels` exactly (same key count, same keys, and same values).
2. **Idempotency**: If no matching override exists, `Reset` must succeed and return `nil`.
3. **Context Cancellation**: Observe `ctx.Done()` before mutating persistent storage.
