# Codegen Partial Evaluation Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Remove the remaining runtime rediscovery logic from generated agent and MCP adapter code so static DSL structure is fully specialized at generation time.

**Architecture:** Push all design-known structural decisions into codegen data builders and templates. Agent config validation should emit direct checks per MCP toolset, MCP resource adapters should emit field-aware query assembly from precomputed metadata, and registry hint maps should be rendered as direct literals or omitted entirely.

**Tech Stack:** Go 1.24, Goa codegen/templates, golden tests, integration tests, testify

---

### Task 1: Specialize MCP Caller Validation In Generated Agent Config

**Files:**
- Modify: `codegen/agent/templates/config.go.tpl`
- Test: `codegen/agent/tests/partial_evaluation_test.go`
- Refresh as needed: `codegen/agent/tests/testdata/golden/mcp_dsl/config.go.golden`
- Refresh as needed: `codegen/agent/tests/testdata/golden/mcp_use/config.go.golden`
- Refresh as needed: `codegen/agent/tests/testdata/golden/mcp_use_alias/config.go.golden`

**Step 1: Write the failing test**

Add a targeted generator test that builds an MCP-using agent and asserts the
generated config:

- does not contain `required := []string`
- does not contain `for _, id := range required`
- does contain one direct check per known MCP toolset constant

**Step 2: Run the red test**

Run: `go test ./codegen/agent/tests -run TestConfigTemplateSpecializesMCPCallerValidation`

Expected: FAIL because the current template still emits the runtime slice and
loop.

**Step 3: Implement the minimal template change**

Rewrite `Validate()` in `config.go.tpl` so it:

- emits no collection-building for known toolset IDs
- emits direct `MCPCallers == nil` / `MCPCallers[Const] == nil` checks
- preserves the current validation behavior and error messages

**Step 4: Verify green**

Run: `go test ./codegen/agent/tests -run 'TestConfigTemplateSpecializesMCPCallerValidation|TestGolden_MCP_DSL|TestGolden_MCP_Use|TestGolden_MCP_UseAlias'`

Expected: PASS, with goldens updated only if the specialized output changes the
rendered config files.

### Task 2: Specialize MCP Resource Query Construction

**Files:**
- Modify: `codegen/mcp/adapter_generator.go`
- Modify: `codegen/mcp/templates/mcp_client_wrapper.go.tpl`
- Test: `codegen/mcp/contract_test.go`
- Regenerate as needed via tests: `integration_tests/fixtures/assistant`

**Step 1: Write the failing test**

Add a generator test for a resource method with query payload fields that
asserts the rendered adapter:

- does not contain `map[string]any`
- does not contain `json.Unmarshal`
- does not contain runtime key sorting
- does contain direct field-aware query assembly for the payload fields,
  including repeated params for array fields

**Step 2: Run the red test**

Run: `go test ./codegen/mcp -run TestGenerateMCPClientAdapter_SpecializesResourceQueryConstruction`

Expected: FAIL because the generated resource wrapper still round-trips through
 JSON and a generic map.

**Step 3: Implement the minimal specialization**

Teach `adapter_generator.go` to precompute resource query metadata:

- deterministic field order
- query key
- scalar vs repeated behavior
- typed payload accessor expression

Update `mcp_client_wrapper.go.tpl` to emit direct query assembly from that
metadata instead of generic JSON/map inspection.

**Step 4: Verify green**

Run: `go test ./codegen/mcp -run 'TestGenerateMCPClientAdapter_SpecializesResourceQueryConstruction|TestPrepareServices|TestGenerate'`

Expected: PASS.

**Step 5: Verify fixture-backed behavior**

Run: `go test ./integration_tests/tests -run TestMCPResources`

Expected: PASS, proving the regenerated assistant fixture still behaves
correctly with the specialized query construction.

### Task 3: Specialize Registry Hint Template Maps

**Files:**
- Modify: `codegen/agent/templates/registry.go.tpl`
- Test: `codegen/agent/tests/partial_evaluation_test.go`
- Refresh as needed: `codegen/agent/tests/testdata/golden/mcp_dsl/registry.go.golden`
- Refresh as needed: `codegen/agent/tests/testdata/golden/mcp_use/registry.go.golden`
- Refresh as needed: `codegen/agent/tests/testdata/golden/mcp_use_alias/registry.go.golden`
- Refresh as needed: `codegen/agent/tests/testdata/golden/example_internal_mcp/bootstrap.go.golden`

**Step 1: Write the failing test**

Extend the partial-evaluation test coverage to assert generated registry code:

- does not lazily initialize `callRaw` / `resultRaw`
- emits direct literal maps when hint templates exist
- omits hint-map code entirely when no hint templates exist

**Step 2: Run the red test**

Run: `go test ./codegen/agent/tests -run TestRegistryTemplateSpecializesHintMaps`

Expected: FAIL because the current template still builds maps lazily at runtime.

**Step 3: Implement the minimal template change**

Rewrite the hint-map section in `registry.go.tpl` so template-time branches emit:

- no hint code when no templates exist
- direct composite literals when templates do exist

Keep runtime behavior unchanged after compilation.

**Step 4: Verify green**

Run: `go test ./codegen/agent/tests -run 'TestRegistryTemplateSpecializesHintMaps|TestGolden_MCP_DSL|TestGolden_MCP_Use|TestGolden_MCP_UseAlias|TestExampleInternal_MCP'`

Expected: PASS, with only intentional golden updates.

### Task 4: Final Formatting And Proof Sweep

**Files:**
- Revisit any file changed in Tasks 1-3

**Step 1: Format touched Go files**

Run: `gofmt -w codegen/mcp/adapter_generator.go codegen/mcp/contract_test.go codegen/agent/tests/partial_evaluation_test.go`

Expected: no output.

**Step 2: Run targeted generator suites**

Run: `go test ./codegen/mcp ./codegen/agent/tests`

Expected: PASS.

**Step 3: Run repository verification**

Run: `make test`

Expected: PASS.

**Step 4: Run integration verification**

Run: `make itest`

Expected: PASS.

**Step 5: Review the diff**

Run: `git diff -- codegen/agent codegen/mcp integration_tests/fixtures/assistant integration_tests/scenarios docs/plans`

Expected: only specialized generator logic, corresponding tests, regenerated
fixture output, and the approved design/plan docs remain.

## Notes

- Do not hand-edit files under `integration_tests/fixtures/assistant/gen/**`;
  let test-driven regeneration update them.
- Keep behavior unchanged unless a current runtime path exists only because the
  generator was previously deferring static structure to runtime.
- Prefer precomputed generator metadata over runtime helper indirection for
  design-known payload/query structure.
