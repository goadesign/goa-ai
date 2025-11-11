## Simplify Tool Identifiers (Global Simple IDs, No Fully-Qualified Names)

### Desired Outcome

- Tools are identified by a single, globally unique simple ID (the DSL `Tool("name", ...)`), not a fully-qualified string like `service.toolset.tool`.
- The runtime routes tool calls based on specs (metadata) rather than parsing tool names.
- Code generation emits simple tool IDs everywhere; toolset IDs remain qualified (e.g., `service.toolset`) for registration and routing.
- Documentation and APIs refer to tool “IDs” (globally unique), not “fully-qualified IDs.”
- The repository compiles cleanly; tests and lint pass.


### Implementation Plan (Reader needs no prior context)

1) Add design-time validation for global tool name uniqueness
   - Implement a validation pass on the agents root expression that walks all toolsets (top-level, agent Used, agent Exported) and gathers tool names. Fail if any duplicate tool name is found.
   - This ensures that simple tool IDs are safe to use globally without qualification.

2) Update identity documentation to reflect simple IDs
   - Update `runtime/agent/tools/ident.go` and `runtime/agent/tools/spec.go` comments to describe “globally unique tool identifier” rather than FQ IDs.
   - Update planner, stream/events, and runtime types comments that reference “fully qualified” tool names.

3) Change the specs builder to emit simple tool IDs and qualified toolset IDs
   - In `codegen/agent/specs_builder.go`, set the tool entry `Name` to the tool’s simple name (DSL name).
   - Set the tool entry `Toolset` field to the toolset’s qualified id (e.g., `service.toolset`) so runtime can route by specs.

4) Update generator templates to use simple tool IDs
   - `templates/used_tools.go.tpl` and `templates/agent_tools.go.tpl`: constants for tool IDs should equal the simple tool ID, not a fully-qualified string.
   - `templates/service_toolset.go.tpl`: keys for hint templates must use simple tool IDs to remain consistent with the new identity.

5) Update MCP code generation to emit simple IDs
   - Where MCP generators populate per-tool specs, use the tool’s simple name as the `ToolSpec.Name`. Keep `ToolSpec.Toolset` equal to the qualified toolset ID.

6) Update example executor stub to switch on simple tool IDs
   - The generated stub switches on `call.Name`. Change those cases to simple IDs.

7) Route tools by spec in the runtime (no name parsing)
   - In `workflow.go` and `activities.go`, replace any `service.toolset.tool` parsing logic with a spec lookup (`Runtime.toolSpec`) to fetch the tool’s toolset ID, then route via `r.toolsets[spec.Toolset]`.

8) Remove the name-splitting helper and call sites
   - Delete `toolsetIdentifier` and update callers to use spec-driven routing.

9) Refresh comments for clarity
   - Replace references to “fully qualified” tool names in runtime/planner/events/types with “globally unique” tool IDs in comments.

10) Update tests to use simple tool IDs
    - Replace any test strings like `svc.ts.tool` with simple tool IDs (e.g., `tool`).
    - Ensure tests that construct or assert against tool IDs align with the new semantics.

11) Lint and test
    - Run `make lint` and `make test`. Fix any issues uncovered until clean.


### Progress Tracker

- [ ] 1. Add global uniqueness validation for tool names
- [ ] 2. Update identity docs and comments (tools.Ident, ToolSpec.Name)
- [ ] 3. Change specs builder (simple tool IDs; qualified toolset IDs)
- [ ] 4. Update generator templates for simple tool IDs
- [ ] 5. Update MCP codegen to simple tool IDs
- [ ] 6. Update example executor stub to switch on simple IDs
- [ ] 7. Implement spec-based routing in runtime (no parsing)
- [ ] 8. Remove name-splitting helper and callers
- [ ] 9. Refresh comments across runtime/planner/events/types
- [ ] 10. Update tests to use simple tool IDs
- [ ] 11. Lint and test; ensure repo is clean


