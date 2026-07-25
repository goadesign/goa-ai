# Registry Admission Redesign — Staged Rollout Plan

This plan sequences the adoption of the registry admission-generation redesign
across every registry consumer. The permanent contract, caller adoption gate,
deployment gate, and one-time data cleanup are owned by
[POST_ROLLOUT_CLEANUP.md](POST_ROLLOUT_CLEANUP.md); this document only orders
the work across repositories and lists every breaking surface a consumer must
adapt to. Aura is the sole known external consumer; within goa-ai the only
binaries are `registry/cmd/registry` and the test suites.

## Breaking surfaces

gRPC contract (registry service):

- `Register` requires `admission_revision` and `provider_incarnation_id` and
  returns `registration_token` plus `lease_duration_ms`.
- `Unregister` requires `expected_registration_token` and returns
  `admission_conflict` on a stale token.
- New `ReleaseProvider` method releases one exact provider-incarnation lease.
- `Pong` requires `provider_incarnation_id`.
- `ToolCallMeta.tool_call_id` is required.
- `CallToolResult` adds required `registration_token` and
  `result_stream_ttl_ms`.
- New typed errors: `admission_blocked` (retryable), `admission_retired`
  (permanent), `admission_conflict` (stale token).

Go library surfaces (hit external consumers harder than the payload changes):

- `toolprovider.Serve` gains a required `Registration` argument; registration
  is no longer a startup step outside `Serve`.
- `provider.Options.Pong` becomes
  `func(ctx, providerID, incarnationID, pingID string) error`.
- `executor.Client.CallTool` returns `toolregistry.ToolCallRef` (identity,
  token, result-stream TTL) instead of a bare tool-use ID; the
  `executor.WithSinkName` option is deleted (executors read results with
  independent oldest-first Readers, no sink/ack/keepalive state).
- Every `runtime/toolregistry` message constructor gains a leading
  `registrationToken` parameter; results and deltas whose token does not match
  the call are dropped by the executor.
- `registry.NewHealthTracker` is unexported; health epoch and last pong live in
  the admission CAS record, and the `<name>:health` rmap plus
  `registry:health:*` / `registry:lease:*` keys are removed.

## Stage 0 — Land goa-ai (no consumer impact)

Merge the redesign into goa-ai `main` and tag a release. Nothing deployed
changes: aura pins a pre-redesign goa-ai version, and the redesigned registry
server is wire-compatible with nothing yet deployed against it, so the tag is
inert until consumers bump.

Exit criteria: `-race` unit and `-tags integration` suites green; this plan and
POST_ROLLOUT_CLEANUP.md merged alongside the code.

## Stage 1 — Port aura against the new module (compile gate)

Bump `goa.design/goa-ai` in aura and port in dependency order:

1. Regenerate `gen/**` (toolset providers pick up token-threaded message
   constructors).
2. Rewrite `shared/clients/toolregistry`: add `ReleaseProvider` to the client
   interface and gRPC wiring, return `ToolCallRef` from `CallTool`, make
   `tool_call_id` required, convert the ponger to the four-argument form,
   delete the startup-only `RegisterToolset` helper, regenerate mocks.
3. Port the eight `toolprovider.Serve` sites (code-interpreter,
   analytics-agent ×2, google-drive, atlas-data ×3, todos) to the
   `Registration{AdmissionRevision, Register, Release}` pattern from
   `codegen/agent/templates/agents_quickstart.go.tpl`.
4. Thread tokens through provider-side wrappers: `shared/toolreplay`
   (replayed results must carry `RegistrationToken`), atlas-data's
   `sessionInjector` error path, and flush code-interpreter's persisted
   `ToolResultMessage` namespace at cutover (pre-cutover records carry no
   token and would be rejected).
5. Registry host (`services/tool-registry`): set `ProviderLeaseDuration`,
   add `ReleaseProvider` to the otel filter allow-list, and rewrite or retire
   `toolset_health.go` (it reads the deleted `registry:health:*` keys and
   would silently report nothing).
6. Read-only consumers: map the new `admission_*` errors in
   `services/front/tool_registry.go`. chat-agent is a pure consumer — its six
   registry-backed executors compile once `shared/clients/toolregistry`
   returns `ToolCallRef`; it registers no toolset.

Exit criteria: aura builds, `./scripts/lint` passes, integration tests green
against a locally run redesigned registry.

## Stage 2 — Deploy plumbing

- Add an `AdmissionRevision` source (env or flag) to all eight provider
  deployments; deployment code never supplies incarnation IDs.
- Audit deployment strategies: RollingUpdate only for identical tokens; use
  Recreate for any schema or admission-revision change (deployment gate in
  POST_ROLLOUT_CLEANUP.md).

## Stage 3 — Hard cutover

Execute POST_ROLLOUT_CLEANUP.md end to end: stop pre-contract providers and
consumers, run the one-time data cleanup (delete `registry:health:*`,
`registry:lease:*`, pre-contract catalog values, unfenced streams), deploy the
redesigned registry, then the ported providers, then consumers. The caller
adoption gate must be fully satisfied before cleanup begins; there is no
mixed-version mode.

## Stage 4 — Post-cutover

- Remove any remaining rollout-era tooling that referenced the deleted keys.
- Keep the retired-token set permanent: it is correctness state, never cleanup
  residue.
