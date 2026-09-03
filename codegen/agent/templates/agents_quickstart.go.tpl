# Welcome to Your Goa-AI Agents! 👋

This guide is your personal co-pilot, generated specifically to help you bring your new AI agents to life. We'll go from the code Goa just created to a running agent in a few simple steps.

> **A Quick Note on This File:**
>
> - **Want to hide me?** No problem! Add `DisableAgentDocs()` to your `API` design and I won't be generated next time.
> - **Safety First:** It's safe to delete this file. It will reappear, updated, after the next `goa gen`.
> - **Golden Rule:** Never edit the `gen/` directory directly. Your design files are the source of truth!

---

## 1. Your Design, At a Glance ✨

Here’s a map of what Goa-AI just built for you based on your `design/*.go` files.

{{- range .Services }}
* **Service `{{ .Service.Name }}`:**
{{- if .Agents }}
{{- range .Agents }}
    * **Agent `{{ .Name }}`** (ID: `{{ .ID }}`):
        * **Mission:** *{{ .Description }}*
        * **Uses Toolsets:**
            {{- if .UsedToolsets }}
            {{- range .UsedToolsets }}
            * `{{ .QualifiedName }}`{{ if .ProviderLabel }} (from remote MCP service `{{ .ProviderLabel }}`){{ end }}
            {{- end }}
            {{- else }}*none*
            {{- end }}
        * **Exports Toolsets:**
            {{- if .ExportedToolsets }}
            {{- range .ExportedToolsets }}
            * `{{ .QualifiedName }}`
            {{- end }}
            {{- else }}*none*
            {{- end }}
        * **Run Policy:**
            * Max Tool Calls: `{{ .RunPolicy.Caps.MaxToolCalls }}`
            * Max Recovery Turns: `{{ .RunPolicy.Caps.EffectiveMaxRecoveryTurns }}`
            * Time Budget: `{{ .RunPolicy.TimeBudget }}`
{{- end }}
    * **Direct Completions:**
        {{- if .Completions }}
        {{- range .Completions }}
        * `{{ .Name }}`
        {{- end }}
        {{- else }}*none*
        {{- end }}
{{- else }}
* This service doesn't define any agents itself, but it might provide tools for others to use!
{{- end }}
{{- end }}

---

## 2. 🚀 The 3-Step Liftoff: Your First Agent Run

The fastest way to run your agent is using the generated example scaffolding.

### Quick Start (Recommended)

```bash
# 1. Generate code and example files
goa gen <module>/design
goa example <module>/design

# 2. Run the generated example
go run ./cmd/<service>/
```

This generates:
- `internal/agents/<service>/bootstrap/bootstrap.go` — Wires runtime and registers that service's agents
- `internal/agents/<agent>/planner/planner.go` — Stub planner (edit to connect your LLM)
- `cmd/<service>/main.go` — Example main that uses the bootstrap
- `gen/<service>/completions/` — Typed completion helpers when your service declares `Completion(...)`

### Understanding the Generated Code

When an unbounded tool declares a payload example and either has no result or
declares a result example, the scaffold demonstrates one complete deterministic
agent turn:

1. `PlanStart` decodes a generated payload example and calls
   `planner.NewToolRequest(gentool.<Tool>Tool(), args)`.
2. `bootstrap.New` registers a deterministic example executor for that toolset.
   The executor validates the payload with the generated codec and returns the
   generated result example, or a successful no-result outcome.
3. The runtime executes the call and invokes `PlanResume` with the correlated
   result.
4. The runtime matches the result to the pending call. `PlanResume` checks the
   tool name, then returns the exact JSON result in the final assistant message.

This path exercises the generated schemas, codecs, executor registration,
runtime tool dispatch, and `PlanStart` → `PlanResume` transition without
requiring model credentials. Replace the planner and example executor together
when connecting real model and service implementations.

If no tool has the examples needed for that demonstration, the generated
planner returns a final greeting without calling a tool. Bounded tools are not
selected because their returned counts and truncation state belong to a real
executor rather than generated sample data.

When a service also declares `Completion(...)` contracts, Goa always generates
`gen/<service>/completions/`. For completions with an authored result example,
the example main also demonstrates both
`Complete<Name>(...)` and `StreamComplete<Name>(...)` using the generated typed
codecs and schema examples.

---

## 3. Meet Your Agents 🤖

Here are the detailed cheat sheets for each agent you designed.

{{- range .Services }}{{- range .Agents }}
<details>
<summary><strong>Agent: <code>{{ .Name }}</code></strong> (ID: <code>{{ .ID }}</code>)</summary>

* **Package:** `{{ .ImportPath }}`
* **Directory:** `{{ .Dir }}`
* **Config Struct:** `{{ .ConfigType }}`
* **Register Function:** `{{ .PackageNames.Register }}(ctx, rt, cfg)`
* **How to Run:**
    * **Sessions are first-class:** ask the service that owns sessions to create `sessionID` before starting a run, and supply a stable run ID for each workflow. The agent runtime receives a storage client but does not administer sessions.
    * **Synchronous (wait for result):**
        ```go
        client := {{ .PackageName }}.NewClient(rt)
        out, err := client.Run(ctx, sessionID, messages, runtime.WithRunID(runID))
        ```
    * **Asynchronous (get a handle):**
        ```go
        client := {{ .PackageName }}.NewClient(rt)
        handle, err := client.Start(ctx, sessionID, messages, runtime.WithRunID(runID))
        ```
* **Workflow Name:** `{{ .Runtime.Workflow.Name }}` (Queue: `{{ .Runtime.Workflow.Queue }}`)

#### Minimal Configuration

{{- $agent := . -}}

```go
cfg := {{ .PackageName }}.{{ .ConfigType }}{
    Planner: myPlanner,
    {{- if .MCPToolsets }}
    MCPCallers: map[string]mcpruntime.Caller{
        {{- range .MCPToolsets }}
        // Expects a caller for the '{{ .SuiteName }}' suite
        {{ $agent.PackageName }}.{{ .ConstName }}: your_mcp_caller_for_{{ .CallerName }},
        {{- end }}
    },
    {{- end }}
}
```
</details>
{{- end }}{{- end }}

---

## 4. 🧠 The Planner: Giving Your Agent a Brain

The `Planner` is where your agent's intelligence lives. It connects to an LLM to decide what to do next. The `StubPlanner` above is great for testing, but here's the correct interface for a real implementation.

```go
type MySmartPlanner struct{}

// PlanStart is called at the beginning of a run.
func (p *MySmartPlanner) PlanStart(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
    // 1. Get an LLM client from the runtime.
    // mc, _ := in.Agent.PlannerModelClient("bedrock")
    
    // 2. Build a prompt from in.Messages.
    
    // 3. Call the LLM and decide whether to call tools or give a final answer.
    return &planner.PlanResult{
        FinalResponse: &planner.FinalResponse{
            Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "I'm ready to help!"}},
			},
        },
    }, nil
}

// PlanResume is called after tools have run, giving the agent new information.
func (p *MySmartPlanner) PlanResume(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
    // 1. Inspect the tool results from in.ToolOutputs.
    // 2. Build a new prompt including the tool results.
    // 3. Call the LLM to decide what to do next.
    return &planner.PlanResult{
        FinalResponse: &planner.FinalResponse{
            Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "The tools have run. Here's what I found..."}},
			},
        },
    }, nil
}
```

---

## 5. 🛠️ Giving Your Agents Tools

Your agents can do useful work by calling other parts of your system. Here's how to wire them up.

#### Local Service-Backed Tools (`BindTo`) — Executor-First

When your tool maps to a service method (via `BindTo`), Goa-AI generates:
- Service-owned tool specs/codecs under `gen/<service>/toolsets/<toolset>/`
- Agent-exported tool specs/codecs under `gen/<service>/agents/<agent>/exports/<toolset>/`
- One typed descriptor factory per tool (for example `SummarizeDocTool()`) pairing the tool identifier with its payload and result codecs, so planners, executors, and eval hooks decode tool JSON without restating the name-to-codec pairing fixed by the design
- Transform helpers (when shapes are compatible): `transforms.go`
- An application-owned executor stub under `internal/agents/<agent>/toolsets/<toolset>/execute.go`

Wire executors using the generated `RegisterUsedToolsets` helper:

```go
// After registering the agent, wire the toolset executors
err := <agentpkg>.RegisterUsedToolsets(ctx, rt,
    <agentpkg>.With<ToolsetName>Executor(myExecutor),
)
if err != nil { panic(err) }
```

Implement the executor's `Execute` function to:
- Switch on `call.Name` for each tool
- Decode `call.Payload` to typed args with the generated typed descriptor (`specs.<Tool>Tool().Payload.FromJSON`); when the tool declares `Inject(...)`, call `specs.Decode<Tool>(call.Payload, *meta, meta.Labels)` so generated code also supplies those server-owned fields
- Optionally use `Init<Tool>MethodPayload`/`Init<Tool>ToolResult` transforms
- Call your service client and return a `planner.ToolResult`

Minimal executor scaffold:

```go
// internal/agents/<agent>/toolsets/<toolset>/execute.go
package <toolset>

import (
    "context"
    "errors"
    "goa.design/goa-ai/runtime/agent/planner"
    "goa.design/goa-ai/runtime/agent/runtime"
    "goa.design/goa-ai/runtime/agent/tools"
    specs "<module>/gen/<service>/toolsets/<toolset>"
)

func Execute(ctx context.Context, meta *runtime.ToolCallMeta, call *runtime.ToolCall) (*runtime.ToolExecutionResult, error) {
    switch call.Name {
    case specs.<Tool>:
        // Decode with the generated typed descriptor: args is the typed
        // payload (e.g. *specs.<ToolPayload>), no type assertion needed.
        args, err := specs.<Tool>Tool().Payload.FromJSON(call.Payload)
        if err != nil {
            var issuer interface {
                Issues() []*tools.FieldIssue
            }
            var issues []*tools.FieldIssue
            if errors.As(err, &issuer) {
                issues = issuer.Issues()
            }
            return runtime.Executed(&planner.ToolResult{
                Name: call.Name,
                Failure: &planner.ToolFailure{
                    Kind: planner.FailureInvalidCall,
                    Error: planner.ToolErrorFromError(err),
                    Recovery: planner.RecoveryDirective{
                        Action: planner.RecoveryCorrectCall,
                        Issues: issues,
                    },
                },
            }), nil
        }
        // Call your service client with args and return the generated result
        // type. Method-backed providers generated in the owning service already
        // contain the required payload and result transforms.
        return runtime.Executed(&planner.ToolResult{
			Name:   call.Name,
			Result: &specs.<ToolReturn>{
				Status: "ok",
			},
		}), nil
    }
    return runtime.Executed(&planner.ToolResult{
        Name: call.Name,
        Failure: &planner.ToolFailure{
            Kind: planner.FailureInvalidCall,
            Error: planner.NewToolError("unknown tool"),
            Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan},
        },
	}), nil
}
```

{{- if .HasServiceProviders }}
---

#### Service-Side Tool Providers (Registry-Routed Execution)

When a toolset is **method-backed** (a tool is declared via `BindTo(...)`) and the toolset is owned by a service, Goa-AI also generates a **tool provider**:

- `gen/<service>/toolsets/<toolset>/provider.go`

The provider implements `HandleToolCall(ctx, msg)` which:

- Decodes the incoming tool payload JSON using the generated payload codec
- Builds the Goa method payload (using the generated transforms)
- Calls the bound service method
- Encodes the tool result JSON (and optional artifact/sidecar) using the generated result codec

To serve tool calls from the registry gateway, run the provider loop inside the owning service process:

```go
// In your service composition root, import the generated toolset package as
// toolsetpkg and construct its provider around the service implementation.
generatedSpecs := toolsetpkg.Specs()
toolSchemas := make([]*registry.ToolSchema, len(generatedSpecs))
for i, spec := range generatedSpecs {
    description := spec.Description
    toolSchemas[i] = &registry.ToolSchema{
        Name:                   string(spec.Name),
        Description:            &description,
        Tags:                   spec.Tags,
        PayloadSchema:          spec.Payload.Schema,
        ExecutionPayloadSchema: spec.ExecutionPayloadSchema,
        ResultSchema:           spec.Result.Schema,
    }
}
handler := toolsetpkg.NewProvider(svcImpl)
podName := mustRequiredEnv("HOSTNAME")
providerID := podName + "/" + toolsetID
admissionRevision := mustRequiredEnv("TOOL_REGISTRY_ADMISSION_REVISION")
go func() {
    err := toolprovider.Serve(ctx, pulseClient, toolsetID, handler,
        toolprovider.Registration{
            AdmissionRevision: admissionRevision,
            Register: func(ctx context.Context, toolset, providerID, incarnationID, admissionRevision string) (toolprovider.RegistrationLease, error) {
                schemaFingerprint, err := toolsetpkg.SchemaFingerprint(toolset)
                if err != nil {
                    return toolprovider.RegistrationLease{}, err
                }
                result, err := registryClient.Register(ctx, &registry.RegisterPayload{
                    Name:              toolset,
                    Tools:             toolSchemas,
                    ProviderID:        providerID,
                    ProviderIncarnationID: incarnationID,
                    AdmissionRevision: admissionRevision,
                    WireProtocolVersion: registrywire.WireProtocolVersion,
                    SchemaFingerprint: schemaFingerprint,
                })
                if err != nil {
                    return toolprovider.RegistrationLease{}, err
                }
                return toolprovider.RegistrationLease{
                    RegistrationToken: result.RegistrationToken,
                    Duration:          time.Duration(result.LeaseDurationMs) * time.Millisecond,
                }, nil
            },
            Drain: func(ctx context.Context, toolset, providerID, incarnationID, expectedToken string, settlementDuration time.Duration) error {
                return registryClient.DrainProvider(ctx, &registry.DrainProviderPayload{
                    Name:                      toolset,
                    ProviderID:                providerID,
                    ProviderIncarnationID:     incarnationID,
                    ExpectedRegistrationToken: expectedToken,
                    SettlementDurationMs:      settlementDuration.Milliseconds(),
                })
            },
            Release: func(ctx context.Context, toolset, providerID, incarnationID, expectedToken string) error {
                return registryClient.ReleaseProvider(ctx, &registry.ReleaseProviderPayload{
                    Name:                      toolset,
                    ProviderID:                providerID,
                    ProviderIncarnationID:     incarnationID,
                    ExpectedRegistrationToken: expectedToken,
                })
            },
            Complete: func(ctx context.Context, toolset, providerID, incarnationID, providerToken, requestEventID string, result registrywire.ToolResultMessage) error {
                resultJSON, err := json.Marshal(result)
                if err != nil {
                    return err
                }
                return registryClient.CompleteToolCall(ctx, &registry.CompleteToolCallPayload{
                    Toolset:                   toolset,
                    ProviderID:                providerID,
                    ProviderIncarnationID:     incarnationID,
                    RegistrationToken:         result.RegistrationToken,
                    ToolUseID:                 result.ToolUseID,
                    ResultJSON:                resultJSON,
                    RequestEventID:            requestEventID,
                    ProviderRegistrationToken: providerToken,
                })
            },
            PublishOutputDelta: func(ctx context.Context, toolset, providerID, incarnationID, providerToken, callToken, toolUseID, requestEventID, stream, delta string) error {
                return registryClient.PublishToolOutputDelta(ctx, &registry.PublishToolOutputDeltaPayload{
                    Toolset:                   toolset,
                    ProviderID:                providerID,
                    ProviderIncarnationID:     incarnationID,
                    ProviderRegistrationToken: providerToken,
                    CallRegistrationToken:     callToken,
                    ToolUseID:                 toolUseID,
                    RequestEventID:            requestEventID,
                    Stream:                    stream,
                    Delta:                     delta,
                })
            },
            ReportOverload: func(ctx context.Context, toolset, providerID, incarnationID, providerToken, callToken, toolUseID, requestEventID string) error {
                return registryClient.ReportToolCallOverload(ctx, &registry.ProviderToolCallClaimPayload{
                    Toolset:                   toolset,
                    ProviderID:                providerID,
                    ProviderIncarnationID:     incarnationID,
                    ProviderRegistrationToken: providerToken,
                    CallRegistrationToken:     callToken,
                    ToolUseID:                 toolUseID,
                    RequestEventID:            requestEventID,
                })
            },
            Claim: func(ctx context.Context, claim toolprovider.ClaimRequest) (toolprovider.ClaimDisposition, error) {
                result, err := registryClient.ClaimToolCall(ctx, &registry.ClaimToolCallPayload{
                    Toolset:                   claim.Toolset,
                    ProviderID:                claim.ProviderID,
                    ProviderIncarnationID:     claim.ProviderIncarnationID,
                    ProviderRegistrationToken: claim.ProviderRegistrationToken,
                    CallRegistrationToken:     claim.CallRegistrationToken,
                    ToolUseID:                 claim.ToolUseID,
                    RequestEventID:            claim.RequestEventID,
                    ClaimOperationID:          claim.OperationID,
                })
                if err != nil {
                    return "", err
                }
                return toolprovider.ClaimDisposition(result.Disposition), nil
            },
        },
        toolprovider.Options{
            ProviderID: providerID,
            Pong: func(ctx context.Context, providerID, incarnationID, pingID string) error {
                return registryClient.Pong(ctx, &registry.PongPayload{
                    PingID:     pingID,
                    Toolset:    toolsetID,
                    ProviderID: providerID,
                    ProviderIncarnationID: incarnationID,
                })
            },
        },
    )
    if err != nil && !errors.Is(err, context.Canceled) {
        panic(err)
    }
}()
```

Notes:

- The registry publishes tool calls to the deterministic stream `toolset:<toolsetID>:requests`. Every provider uses the canonical consumer group and submits terminal results to the registry, which publishes the canonical event on `result:<toolUseID>`.
- `Serve` obtains an immutable registry dispatch claim before invoking a method, then injects the canonical call identity into its context. Pulse redelivery returns retained terminal/claimed state and never repeats handler execution.
- Providers are generated only when the toolset has at least one **method-backed** tool (and the toolset is not registry-backed).
- Import `goa.design/goa-ai/runtime/toolregistry` as `registrywire`. Every provider Register payload must set `WireProtocolVersion: registrywire.WireProtocolVersion`; the registry rejects missing or mismatched versions before admission.
- Registry-backed consumer clients must set the same `WireProtocolVersion` on every CallTool and RetryTool payload; the registry rejects mismatches before catalog lookup or publication. CallTool performs initial admission. When the executor receives `provider_overloaded` retry control, its RetryTool implementation must pass the original `ToolCallRef.RegistrationToken` as `ExpectedRegistrationToken`; the registry rejects admission rollover and never republishes through a replacement provider.
- `AdmissionRevision` is required and immutable for one fenced admission. Reuse it for scaling and same-contract rolling updates; change it only when schema or rollout intent needs a new execution fence. `Registration.Register` passes it to the typed registry payload and returns `RegistrationToken` plus `LeaseDurationMs`.
- `Serve` generates one UUID incarnation, opens the Pulse request stream, registers that exact provider incarnation, and only then creates the canonical shared request consumer-group sink. It renews from one third of the granted duration. On exit it stops renewal, marks each exact token/incarnation lease draining, and closes sink intake. Draining excludes the lease from new publication while preserving authority to claim and settle already-delivered work. It then settles the local queue and results through the registry, drains acknowledgements, and releases only after clean settlement; otherwise lease expiry is the durable fallback. Treat `context.Canceled` as normal process shutdown.
- `RegistrationToken` is the deterministic SHA-256 admission-generation fence derived from the registry wire protocol version, canonical generated schema bytes, and `AdmissionRevision`, not a secret. Its canonical wire form is lowercase 64-hex. Every success, error, and best-effort output delta echoes the call token. Providers complete stale queued calls with `stale_registration`; independent executor readers ignore mismatched late deltas/results and continue waiting for the exact tool-use ID and token pair.
- The gateway derives the global transport `ToolUseID` from required run ID plus required model/provider call ID. One call record owns immutable identity, terminal state, a Redis-selected execution deadline no longer than `MaxToolCallWait`, and a later bounded result-history expiration. Handler contexts and executor waiting use the execution deadline; result streams and canonical terminals use retention. Unresolved claims atomically settle to `outcome_unknown` before retention expires. Result consumers use independent oldest-first Pulse Readers, so sequential and concurrent retries each replay the same retained events without acknowledgement or consumer-group metadata. A full provider queue publishes top-level retry control with reason `provider_overloaded`, bounded delay, and no planner failure; overload reporting is idempotent per request event. RetryTool attaches to the existing immutable admission and serializes delayed republication only while that exact token remains active.
- Generated handlers receive the call execution-deadline context. Custom handlers must honor cancellation; handler-level idempotency is not required for registry redelivery because dispatch ownership never transfers.
- Health pings carry the admission token and catalog-owned membership epoch; pongs authenticate the exact provider incarnation and update the same CAS admission record. A zero-lease transition advances the epoch and resets pong freshness, so an old process cannot authenticate a later lifecycle.
- Use RollingUpdate only when every replica has the same registration token. Use Recreate for a different schema or admission revision: graceful release enables immediate server-owned handoff, while crashed providers block the new admission until lease expiry. Retirement and replacement permanently retain every prior token as correctness state, so A→B cannot resurrect A; this set grows with distinct admissions and must not be truncated. `Unregister` is reserved for intentional retirement.
{{- end }}

#### Connecting to Remote Services (MCP)

If your agent uses tools from another service via MCP (`Use(MCPToolset(...))`):

1.  Get the generated JSON-RPC client for the remote MCP service.
2.  Use the generated MCP adapter to make an `mcpruntime.Caller`.
3.  Pass it to your agent's config, using the generated constant for the key.

```go
// 1. Get the generated JSON-RPC client for the remote MCP service.
remoteClient := <mcp_jsonrpc_client_pkg>.NewClient(/* your endpoints */)

// 2. Identify this program, initialize the MCP session, and build the runtime caller.
clientInfo := mcpruntime.ClientInfo{
    Name:    "<client_name>",
    Version: "<client_version>",
}
caller, err := <mcp_jsonrpc_client_pkg>.NewCaller(ctx, remoteClient, clientInfo)
if err != nil {
    return fmt.Errorf("initialize MCP caller: %w", err)
}

// 3. Supply it in the agent config.
cfg := <agentpkg>.<AgentConfig>{
    Planner: myPlanner,
    MCPCallers: map[string]mcpruntime.Caller{
        <agentpkg>.<ToolsetIDConst>: caller, // e.g., "assistant.assistant-mcp"
    },
}
```

---
<details>
<summary><strong>Click to see a detailed reference of your agent's toolboxes...</strong></summary>

## 6. Your Agent's Toolbox: A Reference

{{- range .Services }}
{{- range .Agents }}

### Agent `{{ .Name }}` Toolsets

* **Tools this agent can USE:**
{{- if .UsedToolsets }}
{{- range .UsedToolsets }}
* **`{{ .QualifiedName }}`** {{ if .ProviderLabel }}(MCP Suite: `{{ .ProviderLabel }}`){{ end }}
{{- if .Tools }}
{{- range .Tools }}
* **Tool: `{{ .QualifiedName }}`**
* *{{ .Description }}*
{{- end }}
{{- end }}
{{- end }}
{{- else }}
* *This agent does not use any toolsets.*
{{- end }}
* **Tools this agent EXPORTS for others to use:**
{{- if .ExportedToolsets }}
{{- range .ExportedToolsets }}
* **`{{ .QualifiedName }}`**
{{- end }}
{{- else }}
* *This agent does not export any toolsets.*
{{- end }}
{{- end }}
{{- end }}
</details>

---

## 7. Agents Calling Agents (The `Exports` Keyword)

When an agent `Exports` a toolset, other agents can call it. Goa-AI generates a special `agenttools` package to make this easy.

```go
// In your main.go, register the exported toolset so others can find it.
// <agenttools>.ToolsetName contains the exact registration route.
reg, err := <agenttools>.NewRegistration(
    rt,
    "You are a helpful specialist assistant.",  // A system prompt for the nested agent (optional)
    // Configure per-tool content (optional). If omitted, the runtime builds a default
    // user message from the payload; override the builder with WithPromptBuilder.
    runtime.WithText(<agenttools>.ToolXYZ, "Please perform the following task: {{"{{"}} . {{"}}"}}"),
)
if err != nil { panic(err) }

// Now this toolset is available in the runtime for other agents to use!
if err := rt.RegisterToolset(reg); err != nil { panic(err) }
```

---

## 8. 🧪 Evaluating Your Agents
{{- if .Suites }}

An evaluation suite is a set of stable scenarios that exercise your agent and grade the outcome, so you can catch regressions before your users do. Your design declares the following suite{{ if gt (len .Suites) 1 }}s{{ end }}:
{{- range .Suites }}

### Suite `{{ .Name }}`{{ if .Agent }} (agent `{{ .Agent }}`){{ end }}

{{ .Description }}
{{- $suite := . }}
{{- range .Scenarios }}

* **`{{ .Name }}`**{{ if .Tags }} (tags: {{ range $i, $t := .Tags }}{{ if $i }}, {{ end }}`{{ $t }}`{{ end }}){{ end }}: {{ .Description }}{{ if .HasInput }} Supply its typed input when constructing the suite in `cmd/{{ $suite.Name }}-evals`.{{ end }}
{{- end }}

Everything typed lives in `gen/evals/{{ .Name }}/`: a `Hooks` interface with one method per scenario, an `Inputs` struct for typed inputs, and (for agent-attached suites) `MustToolContract` to assert against the agent's reachable tool contracts. `goa example` scaffolds an application-owned command at `cmd/{{ .Name }}-evals/main.go` **once**; it is yours to edit and is never overwritten.

Implement each hook to run the real agent, then return the evidence to grade:

* **Checks** are deterministic facts: which tools were called, with which arguments, whether the run completed.
* **Claims** are plain-language statements about the reply ("the answer names the capital of Japan") graded by a model judge.

Don't recompute trajectory facts by hand. Bridge the runtime's event bus into
an `evidence.Collector` (package `goa.design/goa-ai/eval/evidence`) while the
agent runs, then declare expectations with the generated typed tool
descriptors — the predicates are compile-checked against the tool's actual
payload and result types:

```go
collector := evidence.NewCollector()
sub, err := streambridge.Register(rt.Bus, sink) // sink filters the scenario's session and calls collector.Consume
// ... run the agent ...
ev, err := collector.Finish()
expect := evidence.Expect{
    Tools: []evidence.Tool{
        evidence.ExpectCall(specs.<Tool>Tool(),
            func(p *specs.<ToolPayload>) error { /* assert arguments */ return nil },
            func(r *specs.<ToolReturn>) error { /* assert result */ return nil },
        ),
    },
}
return eval.Result{Checks: expect.Checks(ev), Claims: claims, Output: ev.Answer}, nil
```

`Expect` grades the causal trajectory (in-order subsequence by default,
`Exact: true` for call-for-call equality), forbidden tools, failure
classifications (`evidence.ExpectFailure`), pending operator confirmations
(`evidence.ExpectConfirmation`), and the terminal workflow phase.

```bash
go run ./cmd/{{ .Name }}-evals                    # run every scenario, JSON report on stdout
go run ./cmd/{{ .Name }}-evals -scenario <id>     # run selected scenarios (repeatable)
go run ./cmd/{{ .Name }}-evals -tag <tag>         # run by tag (repeatable)
```

The command exits non-zero when any scenario fails, so it drops straight into CI. Deterministic-only suites can pass a `nil` judge; as soon as a hook returns claims, wire a real judge (see `goa.design/goa-ai/eval/judge`) backed by your model client.
{{- end }}
{{- else }}

Your design doesn't declare any evaluation suites yet. An evaluation suite is a set of stable scenarios that exercise your agent and grade the outcome, so you can catch regressions before your users do. Declare one inside an `Agent` block:

```go
import . "goa.design/goa-ai/eval/dsl"

Agent("chat", "Friendly Q&A assistant", func() {
    // ... toolsets ...
    Suite("chat_quality", func() {
        Description("Evaluates the chat agent end to end.")
        Scenario("greeting_reply", func() {
            Description("The agent produces a final reply to a greeting.")
            Tags("smoke")
        })
    })
})
```

Rerun `goa gen` to get a typed harness under `gen/evals/<suite>/` (one hook per scenario) and `goa example` to scaffold a runnable `cmd/<suite>-evals` command. Each hook runs your real agent and returns deterministic checks plus model-graded claims.
{{- end }}

---

## 9. Ready for Prime Time: Advanced Features 🔭

* **Sessions & Runs:** Sessions are explicit and owned by the host service. Create and end them through that service. Runs (`client.Run`/`client.Start`) require an active session.
* **Session-Owned Streaming (for UIs):** In production, stream consumers should attach to the **session-owned stream** (`session/<session_id>`) and filter by `run_id`. Close SSE when you observe a `run_stream_end` event for the attached run ID. Nested agent runs emit `child_run_linked` links and their own `run_stream_end`; parent runs only emit `run_stream_end` after all child runs have ended.
* **Asynchronous Runs:** Use `client.Start()` to get a workflow handle. This is great for long-running tasks, cancellation, and non-interactive integrations.
* **Human Input:** Clarifications, confirmations, and external results end the current workflow with a typed suspension. Call `PrepareContinuation` with the new workflow ID, call `MarshalBinary`, then store that ID from `RunID()` alongside the prepared bytes and accepted answer in one application database transaction. A later process can load those bytes, call `ParsePreparedRun`, and submit the result with `StartPrepared`.
* **Policies & Caps:** The `RunPolicy` in your design (max tool calls, time budgets) is automatically enforced by the runtime.
* **Persistence & Observability:** The `runtime.New` function requires one `storage.Store` that owns run metadata, continuation checkpoints, and ordered run records. Options configure the engine, memory, streaming, and telemetry.
* **Temporal DataConverter:** The Temporal engine always installs its strict, bounded data converter. Applications provide connection and namespace settings through `ClientOptions`; they cannot replace the workflow data contract.
* **Registries & Discovery:** When you declare registries and `FromRegistry(...)` toolsets in your DSL, Goa-AI generates typed registry HTTP clients under `gen/<svc>/registry/<name>/` plus per-toolset specs helpers such as `DiscoverAndPopulate` and `Specs`. Use the generated `<Toolset>ToolsetName` constant from the agent or service package when registering the discovered tools with `runtime.ToolsetRegistration`.

```go
// Example of production-ready runtime options
rt := runtime.New(runtimeStore,
    // runtime.WithEngine(myTemporalEngine),
    // runtime.WithMemoryStore(myMongoMemoryStore),
    // runtime.WithStream(myEventStreamSink),
)
```

Example: constructing a Temporal worker engine:

```go
import (
    "goa.design/goa-ai/runtime/agent/engine/temporal"
    "go.temporal.io/sdk/client"
)

eng, err := temporal.NewWorker(temporal.Options{
    ClientOptions: &client.Options{
        HostPort:      "127.0.0.1:7233",
        Namespace:     "default",
    },
    WorkerOptions: temporal.WorkerOptions{
        TaskQueue: "<service>_<agent>_workflow",
    },
})
if err != nil {
    panic(err)
}
defer eng.Close()
```

---

## 10. 📜 The Golden Rules: Working with Codegen

* ✍️ **Design First:** Always make changes in your `design/*.go` files.
* 🔄 **Regenerate:** Run `goa gen <module>/design` to apply your changes.
* 🚫 **Hands Off `gen/`:** Never edit the `gen/` directory by hand. Your changes will be overwritten!

---

## 11. 🤔 Stuck? Common Questions & Fixes

* **Error: "runtime not initialized"**
* **Fix:** Ensure you register agents with the same runtime instance you use to start runs.
* **Error: "agent not registered"**
    * **Fix:** Check that `Register<AgentName>(...)` was called successfully for that agent before you tried to run it.
* **Error: "session id is required"**
    * **Fix:** Always provide a unique, non-empty string for the `sessionID` when calling `agent.Run(...)`.
* **Error: "session not found"**
    * **Fix:** Sessions are explicit. Create the session through the host service before starting runs under that session ID.
* **Error: "mcp caller is required for <suite>"**
    * **Fix:** Your agent's config is missing an entry in the `MCPCallers` map for the specified toolset ID. See section 5.
* **Agent-as-Tool isn't working?**
    * **Fix:** Ensure you've provided `WithText` or `WithTemplate` for **every single tool** in the exported toolset when calling `NewRegistration`.
