# Model Gateway (Provider-Agnostic Inference Service)

This guide shows how to expose a centralized inference gateway as a Goa service
without coupling planners to transports or provider SDKs. Planners keep using the
runtime `model.Client` API; the gateway wraps a concrete provider and runs optional
middlewares (policy, safety, retries, metrics).

## When to Use

- You want a single place to apply provider policy (thinking negotiation, allowlists,
  safety/redaction, retry/rate limits) and share it across services.
- You want to deploy models behind an HTTP/GRPC boundary, but keep planners simple.

## Core Pieces

- API types (design-time): `apitypes/design/model.go`
  - `LLMRequest`, `LLMMessage`, `ToolDefinition`, `LLMChunk`
- Server adapter (this repo): `features/model/gateway`
  - `NewServer(WithProvider(...), WithUnary(...), WithStream(...))`
  - Composable middleware chains; no policy baked-in
- Remote client (this repo): `features/model/gateway`
  - `NewRemoteClient` implements `model.Client` via normalized RPC funcs

## Service Design (Example)

Use the shared types in your Goa design to expose a gateway service:

```go
var _ = Service("model_gateway", func() {
    Method("complete", func() {
        Payload(apitypesmodel.LLMRequest)
        Result(apitypesmodel.LLMMessage)
        GRPC(func() {})
    })
    Method("stream", func() {
        Payload(apitypesmodel.LLMRequest)
        StreamingResult(apitypesmodel.LLMChunk)
        GRPC(func() {})
    })
})
```

## Server Wiring

```go
// provider: choose a concrete implementation (Bedrock/OpenAI)
prov, _ := bedrock.New(bedrock.Options{Runtime: awsClient, Model: "anthropic.claude-3-5-sonnet-20241022"})

// compose middlewares (all optional)
logMW := func(next gateway.UnaryHandler) gateway.UnaryHandler {
    return func(ctx context.Context, req model.Request) (model.Response, error) {
        start := time.Now()
        res, err := next(ctx, req)
        _ = start; _ = res; _ = err // add your logger here
        return res, err
    }
}

srv, _ := gateway.NewServer(
    gateway.WithProvider(prov),
    gateway.WithUnary(logMW),
    gateway.WithStream(), // stream middlewares here
)

// In your generated Goa service implementation:
// - Convert gen.LLMRequest -> model.Request (generated ConvertTo)
// - Call srv.Complete / srv.Stream
// - Convert model.* -> gen.LLMMessage / gen.LLMChunk (generated CreateFrom)
```

## Remote Client (Planner-Side) Wiring

If planners run in a different process, register a model via a remote client.

```go
// completeFn/streamFn: thin shims around your generated client
rc := gateway.NewRemoteClient(completeFn, streamFn)
rt.RegisterModel("default", rc)

// planner code remains unchanged
mc, _ := agent.ModelClient("default")
stream, _ := mc.Stream(ctx, model.Request{Model: "…", Messages: msgs, Tools: defs})
```

## Choosing Unary vs Streaming

- Planners choose: `model.Client.Stream` for incremental UX; `Complete` for final-only.
- Gateway exposes both endpoints; callers pick which to hit.
- If streaming is unsupported, return `model.ErrStreamingUnsupported` and optionally
  degrade to unary in client middleware.

## Middleware Ideas (Optional)

- Policy (allowlist/clarify-only), Safety/Redaction, Retry/Backoff, Rate limits,
  Metrics/Tracing, Thinking mode negotiation.

## Notes

- Keep the gateway adapter transport-agnostic; Goa-generated transports call it.
- Avoid duplicating schema logic—derive tool schemas from generated tool specs.

