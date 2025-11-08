## Chat Agent Consumer-Only Toolsets: Comply With Exporters

This note proposes a small but high‑leverage refinement to goa‑ai so consumer agents “just work” with provider toolsets, without manual namespace mapping. It keeps strong contracts, simplifies registration, and aligns what the model sees with what actually executes.

### Desired outcome

- Default behavior: When an agent Uses a toolset exported by another service, the public tool IDs surfaced to the model are the provider’s canonical IDs (service.toolset.tool). No mapping required.
- Generated helpers: The generated `specs` package exposes an explicit `AdvertisedSpecs()` function returning the full, sorted list of tool specs the planner should advertise. Apps do not hand‑craft tool lists.
- Backward clarity: If a consumer wants to publish its own namespaced facade, we can add that later as a separate, explicit feature (not part of this change).

What users do after this change:
- Keep using the DSL `Uses(...)` for provider toolsets and `Exports(...)` for their own.
- In planners, return the generated `specs.AdvertisedSpecs()` directly.
- Register toolsets using the existing typed helpers; no aliasing or `reg.Name/reg.Specs` overrides.

### Plan (reader need not know prior context)

1) Codegen: default provider IDs for Used toolsets
- Change generator so `Uses(...)` emits tool IDs qualified by the provider service (e.g., `atlas_data_agent.ada.*`) instead of the consumer’s service.
- This is computed using the already available `SourceServiceName` for a toolset; only apply when the toolset is externally sourced (method‑backed or external MCP), not when the tools are truly local.

2) Codegen: explicit advertised specs helper
- In the aggregated specs package (`gen/<svc>/agents/<agent>/specs`), add:
  - `AdvertisedSpecs() []tools.ToolSpec` returning a copy of the aggregated specs. Planners use this directly.

3) Tests: update goldens minimally
- Update aggregated specs goldens to include the new function.
- Existing per‑tool specs remain valid; most tests should remain unchanged because service‑local bindings keep their current names.

4) Docs: developer guidance
- Add a brief note for planners: use `specs.AdvertisedSpecs()` to surface tools to the model; avoid manual lists.
- Clarify the “why”: provider exports define the canonical contract; consumers comply by default.

5) Lint and unit tests
- Run `make lint` and `make test`. Fix style or test expectations where needed.

### Progress tracker

- [x] 1. Default provider IDs for Used toolsets in generator
- [x] 2. Add `AdvertisedSpecs()` to aggregated specs template
- [x] 3. Update aggregated specs golden files
- [x] 4. Planner guidance in docs
- [x] 5. Lint clean
- [x] 6. Unit tests pass


