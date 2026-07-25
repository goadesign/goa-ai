# Registry Hard-Cutover Cleanup

This runbook is only for the one-time cutover from pre-admission registry data
to the server-owned admission record. It is not a recurring deployment
procedure and must not become runtime fallback logic.

## Permanent Contract

Keep all of these mechanisms after cleanup:

- One exact-CAS `registry:toolset:<toolset>` record owns active/retired state,
  toolset metadata, canonical schema fingerprint, admission revision,
  registration token, Redis `RegisteredAt`, the provider lease map, and the
  permanent exact set of every retired registration token.
- `AdmissionRevision` is required. Every replica of one fenced admission shares
  it. Same-contract scaling and RollingUpdate reuse it; a new schema or
  intentionally new execution fence changes it.
- `RegistrationToken` remains the deterministic lowercase SHA-256 identity
  derived from canonical generated schema bytes and admission revision.
- `toolprovider.Serve` opens Pulse before registering and cannot claim calls
  before admission. It renews from the granted lease duration.
- `Serve` stops claiming, boundedly closes the sink, closes work intake, settles
  workers/results, and drains acknowledgements. It boundedly retries exact
  `ReleaseProvider` only after settlement succeeds; otherwise expiry is the
  durable fallback.
- A different token receives retryable `admission_blocked` while any Redis-time
  lease remains and atomically replaces the record after all leases disappear.
- `Unregister` remains only intentional retirement. It is never a rollout step.
- Calls, terminal results, and output deltas retain exact
  `(tool_use_id, registration_token)` fencing.
- The gateway derives global transport `tool_use_id` from required run ID plus
  model/provider call ID; model/provider identity remains metadata.
- Queue saturation publishes transient `provider_overloaded` with bounded
  retry-after before acknowledgement; registry call admission serializes
  republication without blocking provider ping intake.
- Pulse retains flat toolset request streams. One registry-selected bounded
  sliding TTL is carried in each call/response and used by every per-call result
  stream handle and call admission. Executors use independent oldest-first
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

Do not cut over until every provider caller has regenerated and:

1. Passes required `Registration` directly to `toolprovider.Serve`.
2. Sends immutable `AdmissionRevision`, stable per-process/toolset `ProviderID`,
   and the runtime-provided incarnation to `Register`.
3. Converts `RegistrationToken` and `LeaseDurationMs` into
   `RegistrationLease`.
4. Implements `Release` with the generated `ReleaseProvider` client using the
   exact admitted token and runtime-provided incarnation.
5. Keeps `Pong` provider-incarnation scoped.
6. Removes startup-only registration, token persistence, rollout
   `Unregister`, and any client-owned stop/start transaction.
7. Treats `admission_blocked` as retryable and `admission_retired` as
   permanent.
8. Uses the token on every call, result, and output delta boundary.
9. Maps `result_stream_ttl_ms` from `CallToolResult` into
   `ToolCallRef.ResultStreamTTL`; it does not configure or destroy result
   streams independently.
10. Supplies a required stable `tool_call_id`; transport retries reuse it.
11. Uses independent Pulse Readers from oldest for result history and does not
    configure a result sink name, acknowledgement, or keepalive.

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
- Call admission, provider deltas/results, and executor Readers all use the
  returned sliding TTL; completed, recreated, and orphaned state disappears
  after inactivity.
- Supported catalog-map, stream, and ticker state reconstruction recovers under
  the same live registry name without process restart. Total destructive loss
  still requires the stopped restore/rebootstrap procedure above.
- No deployment persists a registration token or calls rollout `Unregister`.

After evidence is archived, delete this runbook. Do not remove the permanent
contract mechanisms listed above.
