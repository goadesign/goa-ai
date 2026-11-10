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
toolSchemas := toolsetpkg.ToolSchemas()
handler := toolsetpkg.NewProvider(svcImpl)
podName := mustRequiredEnv("HOSTNAME")
providerID := podName + "/" + toolsetID
admissionRevision := mustRequiredEnv("TOOL_REGISTRY_ADMISSION_REVISION")
if err := registrywire.ValidateAdmissionRevision(admissionRevision); err != nil {
    return err
}
go func() {
    err := toolprovider.Serve(ctx, pulseClient, toolsetID, handler,
        toolprovider.Registration{
            Register: func(ctx context.Context, toolset, providerID, incarnationID string) (toolprovider.RegistrationLease, error) {
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
            Renew: func(ctx context.Context, toolset, providerID, incarnationID, expectedToken string) (time.Duration, error) {
                result, err := registryClient.RenewProvider(ctx, &registry.RenewProviderPayload{
                    Name:                      toolset,
                    ProviderID:                providerID,
                    ProviderIncarnationID:     incarnationID,
                    ExpectedRegistrationToken: expectedToken,
                })
                if err != nil {
                    return 0, err
                }
                return time.Duration(result.LeaseDurationMs) * time.Millisecond, nil
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
            Complete: func(ctx context.Context, toolset, providerID, incarnationID, providerToken, requestEventID string, result registrywire.ToolResultMessage) (bool, error) {
                resultJSON, err := json.Marshal(result)
                if err != nil {
                    return false, err
                }
                completion, err := registryClient.CompleteToolCall(ctx, &registry.CompleteToolCallPayload{
                    Toolset:                   toolset,
                    ProviderID:                providerID,
                    ProviderIncarnationID:     incarnationID,
                    RegistrationToken:         result.RegistrationToken,
                    ToolUseID:                 result.ToolUseID,
                    ResultJSON:                resultJSON,
                    RequestEventID:            requestEventID,
                    ProviderRegistrationToken: providerToken,
                })
                if err != nil {
                    return false, err
                }
                return completion.Accepted, nil
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
- `AdmissionRevision` is required and immutable for one fenced admission. Reuse it for scaling and same-contract rolling updates; change it only when schema or rollout intent needs a new execution fence. Validate it before `Serve` starts, then capture it in the `Registration.Register` closure that sends the typed registry payload and returns `RegistrationToken` plus `LeaseDurationMs`. The generic callback receives no revision argument. A provider attaching to a declaration instead captures its exact token and calls `AttachProvider`; it does not invent a revision.
- `Serve` generates one UUID incarnation, registers its generated definitions at startup, and only then creates the shared request sink. The required `Registration.Renew` calls `RenewProvider` with the exact lease identity and returns only its duration; it never sends schemas or changes the token. Renewal starts around one third of the granted duration. `provider_lease_lost` is terminal and never triggers re-registration. On exit, Serve cancels renewal, marks its lease draining, closes intake, settles accepted work and acknowledgements, then releases the exact lease. An in-flight renewal preserves draining and any longer settlement deadline. Callbacks must honor cancellation; cleanup failures remain errors.
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

// 2. Identify this program and declare only input modes this host can answer.
clientInfo := mcpruntime.ClientInfo{Name: "<client_name>", Version: "<client_version>"}
caller, err := <mcp_jsonrpc_client_pkg>.NewCaller(remoteClient, clientInfo, mcpruntime.InputSupport{}, mcpruntime.HTTPRetryPolicy{})
if err != nil {
    return err
}

// 3. Register one executable binding, then register the agent definition.
executor := <generated_mcp_executor_pkg>.NewMCPExecutor(caller)
if err := <agentpkg>.RegisterUsedToolsets(ctx, rt, <agentpkg>.<WithToolsetExecutor>(executor)); err != nil {
    return err
}
cfg := <agentpkg>.<AgentConfig>{Planner: myPlanner}

```

