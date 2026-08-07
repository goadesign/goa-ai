# Registry Admission and Catalog Cutovers

This runbook owns the permanent hard-cutover contract for registry admission
and persisted catalog changes. The legacy-data cleanup section applies only to
the one-time move from pre-admission data. Neither path may become runtime
fallback logic.

## Permanent Contract

Keep all of these mechanisms after cleanup:

- One exact-CAS `registry:toolset:<toolset>` record owns active/retired state,
  toolset metadata, wire protocol version, canonical schema fingerprint,
  admission revision, registration token, Redis `RegisteredAt`, the provider
  lease map, and the permanent exact set of every retired registration token.
- Registry construction authoritatively reads and strictly validates every
  catalog entry before health tracking or serving. One incompatible record
  keeps the registry unready and names every affected key for offline cleanup.
- `AdmissionRevision` is required. Every replica of one fenced admission shares
  it. Same-contract scaling and RollingUpdate reuse it; a new schema or
  intentionally new execution fence changes it.
- `WireProtocolVersion` is required on every `Register` and `CallTool`. It is a
  hard compatibility fence, not capability negotiation; the registry rejects
  any other version before admission, health checks, or call publication.
- `RegistrationToken` remains the deterministic lowercase SHA-256 identity
  derived from canonical generated schema bytes, admission revision, and
  `WireProtocolVersion`.
- `toolprovider.Serve` opens Pulse before registering and cannot claim calls
  before admission. It renews from the granted lease duration.
- `Serve` stops renewal and marks each exact token/incarnation lease draining
  before closing the canonical shared sink. Draining leases receive no new
  calls but retain settlement authority. It closes work intake, settles
  workers/results through the registry, drains acknowledgements, and boundedly
  retries exact `ReleaseProvider` only after settlement succeeds; otherwise
  expiry is the durable fallback.
- A different token receives retryable `admission_blocked` while any Redis-time
  lease remains and atomically replaces the record after all leases disappear.
- `Unregister` remains only intentional retirement. It is never a rollout step.
- Calls, terminal results, and output deltas retain exact
  `(tool_use_id, registration_token)` fencing.
- The gateway derives global transport `tool_use_id` from required run ID plus
  model/provider call ID; model/provider identity remains metadata.
- Queue saturation publishes top-level retry control with reason
  `provider_overloaded` and bounded retry-after before acknowledgement;
  it carries no planner failure. `RetryTool` requires the original still-active
  registration token and existing call admission, then serializes
  republication without blocking provider ping intake. It never binds retry to
  a replacement provider.
- Pulse retains flat toolset request streams. One registry-owned call record
  carries the Redis-selected absolute expiration and terminal state. Terminal
  result append and terminal state commit are one fenced Redis operation.
  Every per-call result stream handle uses that deadline. Executors use independent oldest-first
  Readers with no result consumer-group/ack/keepalive state and never eagerly
  destroy result streams.
- Different-token deployments use Recreate. Incompatible admissions must never
  overlap.
- Runtime-generated provider incarnation IDs fence Register, Pong, and Release;
  deployment code never supplies them.
- Health epoch and last pong live in the admission CAS record and reset when
  leases transition from nonzero to zero.
- Retirement and replacement atomically retain every prior token. This
  permanent correctness set grows with distinct admissions and must never be
  truncated, probabilistically compacted, or removed during routine cleanup.

## Caller Adoption Gate

Do not cut over until every provider and registry consumer has regenerated and:

1. Passes required `Registration` directly to `toolprovider.Serve`.
2. Sends the generated `WireProtocolVersion`, immutable `AdmissionRevision`,
   stable per-process/toolset `ProviderID`, and the runtime-provided incarnation
   to `Register`.
3. Converts `RegistrationToken` and `LeaseDurationMs` into
   `RegistrationLease`.
4. Implements `Drain` and `Release` with the generated registry clients using
   the exact admitted token and runtime-provided incarnation.
5. Implements `Complete` so the registry atomically publishes terminal results.
6. Keeps `Pong` provider-incarnation scoped.
7. Removes startup-only registration, token persistence, rollout
   `Unregister`, and any client-owned stop/start transaction.
8. Treats `admission_blocked` as retryable and `admission_retired` as
   permanent.
9. Uses the token on every call, result, and output delta boundary.
10. Maps `execution_deadline` and `result_stream_expires_at` from
   `CallToolResult` into the matching `ToolCallRef` fields. Executors wait only
   through execution deadline and do not configure or destroy result streams
   independently.
11. Supplies a required stable `tool_call_id`; transport retries reuse it.
12. Uses independent Pulse Readers from oldest for result history and does not
    configure a result sink name, acknowledgement, or keepalive.
13. Sends the generated `WireProtocolVersion` on every `CallTool` and
    `RetryTool`; it never retries with another version or interprets version
    rejection as provider unavailability.
14. Implements executor `RetryTool` with the generated registry operation,
    passing the original `ToolCallRef.RegistrationToken` as
    `ExpectedRegistrationToken`. It never calls `CallTool` to handle overload.

## Deployment Gate

- RollingUpdate is allowed only when old and new pods produce the same token.
- Use Recreate for any schema or admission-revision change.
- Graceful pods release their exact leases, allowing immediate admission
  replacement.
- Crashed pods block replacement only until their Redis-time lease expires.
- Do not persist registration tokens in Kubernetes resources.
- Do not call `Unregister` during rollout.

## One-Time Data Cleanup

Stop all pre-contract providers and consumers before deleting old data. Keep the
final registry offline until cleanup and caller adoption complete.

Remove legacy fields from the registry maps:

```text
registry:health:<toolset>
registry:health:<toolset>:<provider-id>
registry:lease:<toolset>:<registration-token>
registry:lease:<toolset>:<registration-token>:<provider-id>
```

Remove pre-contract catalog values that lack the complete final admission
record. Remove old generation-suffixed or unfenced toolset request streams and
old per-call result streams containing messages without canonical registration
tokens.

Do not use a raw total Redis reset as this cleanup mechanism. It erases rmap
revision history and permanent retirement tombstones, so a live process cannot
prove anti-resurrection.

If total destructive loss occurs, choose one stopped recovery path:

1. **Restore** (preferred): stop registries, providers, and consumers; restore
   the catalog/rmap backup as one consistent Redis snapshot; verify every
   active token and retired-token tombstone; then resume registries, same-token
   providers, health, and consumers in that order.
2. **Deliberate rebootstrap** (backup unavailable): stop every registry,
   provider, and consumer; prove that no incompatible provider process remains;
   archive every previously issued admission revision as permanently forbidden;
   initialize empty owned maps; select a fresh never-before-used revision; start
   one registry and one provider; verify the reconstructed catalog/token/lease
   and fresh ping/pong; then scale only that exact admission and finally resume
   consumers. This creates a new operational history; it does not recover lost
   tombstones.

Never resume a provider against empty Redis while another admission may still
run, and never describe raw `FLUSHDB` under live processes as recovery.

Use the owning Pulse/rmap administration path for map mutation. Do not add
runtime schema guessing, legacy decoders, key scans, or compatibility fallbacks
to perform this cleanup.

## Cutover

1. Back up the affected Redis keyspaces.
2. Stop old registry, provider, and consumer deployments.
3. Remove legacy map fields and unfenced stream data.
4. Deploy the final registry.
5. Deploy same-admission provider replicas with one approved revision.
6. Wait for registration and current-epoch health.
7. Start consumers.
8. Exercise one exact-token call, delta, and result path per toolset.

## Evidence

Retain evidence that:

- `Register` returns a canonical 64-hex token and positive lease duration.
- The catalog value contains all final fields, embedded provider leases, and
  health epoch/last pong plus the permanent retired-token set.
- Same-token replicas add, renew, and release independently without token or
  `RegisteredAt` churn.
- Different-token registration is blocked until graceful release or expiry and
  then replaces by exact CAS.
- Retirement hides discovery/routing, stale retirement conflicts, and A→B→A
  rejects the original A token while a fresh revision may activate.
- Delayed release from an old incarnation cannot remove a replacement
  incarnation with the same stable provider ID.
- Zero-lease transitions advance health epoch/reset pong, and old pongs cannot
  authenticate re-registration.
- Executors ignore stale reused-ID deltas/results; malformed exact terminal
  events fail immediately. Infrastructure and exhausted overload paths map to
  retryable tool-unavailable.
- Same run/call retries derive one transport ID, equal call IDs in different
  runs differ, and no result stream is eagerly destroyed.
- Cross-node concurrent/sequential retries create one admitted publication and
  every independent Reader replays retained terminal history. Redis stream
  inspection shows no result consumer groups or pending entries.
- Overload retry republishes only while the original registration token remains
  active; an A→B admission rollover returns `admission_conflict` and publishes
  no B call.
- Call admission, provider terminal results, and executor Readers all use the
  returned absolute expiration; completed, recreated, and orphaned state
  disappears at that deadline.
- Supported catalog-map, stream, and ticker state reconstruction recovers under
  the same live registry name without process restart. Total destructive loss
  still requires the stopped restore/rebootstrap procedure above.
- No deployment persists a registration token or calls rollout `Unregister`.

Archive the one-time cleanup evidence after the legacy cutover. Keep this
runbook as the permanent contract for future admission and catalog cutovers.
