# goa-ai Tool Registry Hardening Backlog

This branch (`wip/full-hardening`) preserves a full tool-registry and provider
hardening pass. The narrow incident fix (provider re-registration after Redis
state loss) is extracted and shipped separately; the admission-generation
redesign below is parked here and MUST land as its own reviewed, coordinated
rollout. This document is the authoritative inventory.

Note: everything on this branch builds against the published
`goa.design/pulse` module; nothing here depends on unpublished Pulse changes.

## Shipping separately (Track B: provider re-registration)

The incident half, to be extracted from this branch:

- Health tracker recovers from Redis consumer-group/state loss instead of
  leaving toolsets permanently unhealthy.
- Providers re-register (catalog entry + health tickers) after Redis loses
  registry state, rather than requiring a manual rollout of every provider.
- `StartPingLoop` is ensure-only/idempotent; renewals no longer restart health
  tickers (which could starve pings).

## Parked admission redesign (this branch)

Implemented here, not yet publishable. Includes a registry gRPC design change
(`registry/design/design.go` + regenerated `registry/gen/`), so landing it is
a coordinated rollout with every registry consumer and provider:

- Registration is a required `Serve` argument; registrar callback must be
  context-compliant.
- Registry-owned schema serialization: pending catalog admission via expiring
  exact CAS ownership, typed conflicts, expiring provider leases renewed by
  `Serve`, so partial registrations never become routable and mixed-schema
  replicas cannot overwrite each other last-writer-wins.
- Tool calls are fenced to schema generations: calls carry the admitted token;
  providers never execute mismatched calls.
- Permanent CAS-owned retired-token tombstones prevent A→B→A token
  resurrection.
- Redis lease deadlines returned as durations with conservative monotonic
  local deadlines (no wall-clock comparison across machines).
- Monotonic pong timestamps; admission revalidated after CAS.
- Shutdown settles sink, workers, results, and acknowledgements before
  release; bounded sink close under a lifecycle shutdown context.
- 256-character call-ID bounds and 24-hour lease maximum at the gRPC boundary.

## Open findings against the parked redesign

- Result-stream identity: `tool_call_id` reused as global transport ID can
  collide across concurrent calls; derive transport ID from
  `run_id + tool_call_id`.
- Bounded provider overload can silently lose calls via approximate stream
  `MAXLEN`; needs atomic call admission with overload retry.
- Result TTL does not compose with long or redelivered calls (default TTL can
  destroy the stream mid-call; final `Add` can recreate an unexpiring stream).
- Manual provider unregister rollout should be replaced with registry-owned
  CAS admission transitions and exact provider lease release.
- Public docs (this repo + goa.design site content) not yet updated for the
  final recovery and registration contracts.

## Backlog candidates from the provider-reregistration ship review (2026-07-23)

Non-blocking findings from the diff-scoped review of the
`provider-reregistration` branch; pre-existing on main or accepted residue:

- `Serve`'s "toolset stream subscription closed" return path does not join
  `wg`/`ackWG` (pre-existing pattern, now also covers the ensure goroutine).
- `EnsureGroup` matches BUSYGROUP by error-string substring (Pulse itself does
  the same; brittle across Redis error-message changes).
- `Health()` resolves the registration token with `context.Background()`
  (pre-existing).
- The `=rev` / `map:<name>:content` / `pulse:stream:` pins to Pulse rmap
  internals are documented and enforced only by integration tests; a
  compile-time or version-pinned guard would fail Pulse upgrades earlier.
- Residual clock-skew caveat in revision repair: if the only node holding a
  fast-clock floor disappears, a later repair under-pins until the wall clock
  overtakes the old pin (self-limiting to the skew duration).
- `TestServerIntegration/register_and_list` can flake (~1/27): Register writes
  via `rmap.Set` while List reads the local replica — an eventual-consistency
  window identical on main.

## Process contract for landing the parked work

Same as Pulse: dedicated changes with written contracts, diff-scoped reviews
against `main`, at most two fix/review cycles per change, live-Redis race and
fault-injection acceptance, and a staged rollout plan for the gRPC design
change across all registry consumers.
