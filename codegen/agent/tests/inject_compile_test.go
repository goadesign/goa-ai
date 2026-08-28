package tests

// inject_compile_test.go compiles the generated output of the mixed
// bound/unbound Inject() scenario with the real Go toolchain. Section-level
// golden assertions cannot catch declared-and-unused variables or gated
// imports going stale, which is exactly how the provider.go "meta declared
// but never used" regression slipped past a green CI: only an actual go
// build of the emitted tree proves the generated packages compile.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
	gcodegen "goa.design/goa/v3/codegen"
)

// buildWithPrepareForGeneratedModule generates against the package path used by
// the temporary module below. Real package paths end in "/gen" because the Goa
// CLI passes "<module>/gen", which keeps both generated service import forms
// identical.
func buildWithPrepareForGeneratedModule(t *testing.T, design func()) []*gcodegen.File {
	t.Helper()
	const genpkg = "generated.local/gen"
	_, roots := testhelpers.RunDesign(t, design)
	require.NoError(t, codegen.Prepare(genpkg, roots))
	files, err := codegen.Generate(genpkg, roots, nil)
	require.NoError(t, err)
	return files
}

// writeGeneratedModuleKeepingGen writes files into a temp module at their
// verbatim generator paths (keeping the gen/ prefix), unlike
// writeGeneratedModule which relocates gen/<svc>/... to <svc>/... for the
// codec-behavior tests. Keeping the prefix makes the on-disk layout match
// the "<module>/gen/..." import paths a real goa gen run produces. Files are
// rendered through gcodegen.File.Render -- the same pipeline `goa gen` uses,
// including gofmt and unused-import pruning -- so the tree compiles exactly
// as a real generation run would.
func writeGeneratedModuleKeepingGen(t *testing.T, files []*gcodegen.File) string {
	t.Helper()
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../../..")
	require.NoError(t, err)
	goMod := "module generated.local\n\ngo 1.24\n\nrequire goa.design/goa-ai v0.0.0\n\nreplace goa.design/goa-ai => " + filepath.ToSlash(repoRoot) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o600))
	for _, file := range files {
		_, err := file.Render(root)
		require.NoErrorf(t, err, "render %s", file.Path)
	}
	return root
}

// applicationExecutorBuildCommand returns a fixed compile command for each
// application executor scenario so test data cannot alter subprocess arguments.
func applicationExecutorBuildCommand(ctx context.Context, scenario string) *exec.Cmd {
	switch scenario {
	case "no result only":
		return exec.CommandContext(ctx, "go", "build", "-mod=mod", "./internal/agents/scribe/toolsets/ops")
	case "result without example":
		return exec.CommandContext(ctx, "go", "build", "-mod=mod", "./internal/agents/scribe/toolsets/profiles")
	default:
		panic("unknown application executor compile scenario: " + scenario)
	}
}

// mcpExecutorBuildCommand returns fixed package arguments for each generated
// MCP executor variant so the subprocess never consumes a dynamic path.
func mcpExecutorBuildCommand(ctx context.Context, scenario string) *exec.Cmd {
	switch scenario {
	case "Goa-backed result", "Goa-backed no result":
		return exec.CommandContext(ctx, "go", "build", "-mod=mod", "./gen/alpha/agents/scribe/core")
	case "aliased Goa-backed executor":
		return exec.CommandContext(ctx, "go", "build", "-mod=mod", "./gen/alpha/agents/scribe/calc_remote")
	case "external inline inject":
		return exec.CommandContext(ctx, "go", "build", "-mod=mod", "./gen/alpha/agents/scribe/remote_search")
	default:
		panic("unknown MCP executor compile scenario: " + scenario)
	}
}

// TestGeneratedApplicationExecutorsCompile proves application-owned executor
// files import rawjson only when their generated implementation decodes an
// authored result example. These two designs exercise the branches that do not:
// tools with no result and a tool whose result has no authored example.
func TestGeneratedApplicationExecutorsCompile(t *testing.T) {
	tests := []struct {
		name        string
		design      func()
		servicePath string
		serviceStub string
		agentStub   string
	}{
		{
			name:        "no result only",
			design:      testscenarios.NoResultMethod(),
			servicePath: "gen/tasks/service_stub.go",
			serviceStub: `package tasks

import "context"

type PurgePayload struct {
	SessionID string
}

type Service interface {
	Purge(context.Context, *PurgePayload) error
	Heartbeat(context.Context) error
}

type Client struct{}

func (c *Client) Purge(context.Context, *PurgePayload) error {
	return nil
}

func (c *Client) Heartbeat(context.Context) error {
	return nil
}
`,
			agentStub: `package scribe

import "goa.design/goa-ai/runtime/agent/runtime"

func NewScribeOpsToolsetRegistration(runtime.ToolCallExecutor) runtime.ToolsetRegistration {
	return runtime.ToolsetRegistration{}
}
`,
		},
		{
			name:        "result without example",
			design:      testscenarios.MethodComplexEmbedded(),
			servicePath: "gen/alpha/service_stub.go",
			serviceStub: `package alpha

import "context"

type Address struct {
	Street string
	City   string
}

type Profile struct {
	ID      string
	Name    *string
	Address *Address
}

type Service interface {
	UpsertProfile(context.Context, *Profile) (*Profile, error)
}

type Client struct{}

func (c *Client) UpsertProfile(context.Context, *Profile) (*Profile, error) {
	return &Profile{}, nil
}
`,
			agentStub: `package scribe

import "goa.design/goa-ai/runtime/agent/runtime"

func NewScribeProfilesToolsetRegistration(runtime.ToolCallExecutor) runtime.ToolsetRegistration {
	return runtime.ToolsetRegistration{}
}
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			files := testhelpers.BuildAndGenerateWithExamplePkg(
				t,
				"generated.local/gen",
				test.design,
			)
			root := writeGeneratedModuleKeepingGen(t, files)
			writeGeneratedPackageTest(t, root, test.servicePath, test.serviceStub)
			writeGeneratedPackageTest(
				t,
				root,
				"gen/alpha/agents/scribe/registration_stub.go",
				test.agentStub,
			)

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			runGeneratedGoTestCommand(
				t,
				root,
				applicationExecutorBuildCommand(ctx, test.name),
			)
		})
	}
}

// TestGeneratedMCPExecutorsCompile asks the Go compiler to type-check each
// available MCP executor shape together with the exact generated specs package
// it imports. The small service stub represents Goa service codegen, which the
// agent generator does not emit.
func TestGeneratedMCPExecutorsCompile(t *testing.T) {
	const calcServiceStub = `package calc

// AddPayload mirrors the method payload emitted by Goa service codegen.
type AddPayload struct {
	A int
	B int
}
`
	tests := []struct {
		name            string
		design          func()
		executorPackage string
		specsPrefix     string
		serviceStub     string
	}{
		{
			name:            "Goa-backed result",
			design:          testscenarios.MCPUse(),
			executorPackage: "gen/alpha/agents/scribe/core",
			specsPrefix:     "gen/calc/toolsets/core/",
			serviceStub:     calcServiceStub,
		},
		{
			name:            "Goa-backed no result",
			design:          testscenarios.MCPUseNoResult(),
			executorPackage: "gen/alpha/agents/scribe/core",
			specsPrefix:     "gen/calc/toolsets/core/",
			serviceStub:     calcServiceStub,
		},
		{
			name:            "aliased Goa-backed executor",
			design:          testscenarios.MCPUseAlias(),
			executorPackage: "gen/alpha/agents/scribe/calc_remote",
			specsPrefix:     "gen/calc/toolsets/calc_remote/",
			serviceStub:     calcServiceStub,
		},
		{
			name:            "external inline inject",
			design:          testscenarios.MCPUseExternalInlineInject(),
			executorPackage: "gen/alpha/agents/scribe/remote_search",
			specsPrefix:     "gen/remote/toolsets/remote_search/",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			files := buildWithPrepareForGeneratedModule(t, test.design)
			compileFiles := make([]*gcodegen.File, 0, len(files))
			executorFile := filepath.ToSlash(filepath.Join(test.executorPackage, "mcp_executor.go"))
			for _, file := range files {
				path := filepath.ToSlash(file.Path)
				if path == executorFile || strings.HasPrefix(path, test.specsPrefix) {
					compileFiles = append(compileFiles, file)
				}
			}
			require.NotEmpty(t, compileFiles)
			root := writeGeneratedModuleKeepingGen(t, compileFiles)
			if test.serviceStub != "" {
				writeGeneratedPackageTest(t, root, "gen/calc/service_stub.go", test.serviceStub)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			runGeneratedGoTestCommand(t, root, mcpExecutorBuildCommand(ctx, test.name))
		})
	}
}

// TestGeneratedMixedInjectPackagesCompile generates the mixed
// bound/unbound Inject() scenario (a non-injecting BindTo tool sharing a
// toolset with a label-injecting unbound tool) into a temp module and
// compiles both generated packages that carry injection code: the toolset
// specs package (codecs, inject.go, transforms, provider.go) and the
// agent-side service executor package. Only the Goa-core service package
// (which agent codegen does not emit) is stubbed; every agent-generated
// file, including http/validate.go, is compiled verbatim.
func TestGeneratedMixedInjectPackagesCompile(t *testing.T) {
	files := buildWithPrepareForGeneratedModule(t, testscenarios.InjectMixedBoundUnboundExample())
	root := writeGeneratedModuleKeepingGen(t, files)

	// Stub the Goa-core service package (emitted by `goa gen`'s service
	// codegen, not by the agent generator): the generated provider,
	// transforms, and service executor import generated.local/gen/catalog for
	// the Service interface, Client, and method payload/result types.
	writeGeneratedPackageTest(t, root, "gen/catalog/service_stub.go", `package catalog

import "context"

// GetDataPayload mirrors the bound method payload emitted by Goa service codegen.
type GetDataPayload struct {
	Query string
}

// GetDataResult mirrors the bound method result emitted by Goa service codegen.
type GetDataResult struct {
	OK bool
}

// Service mirrors the Goa service interface referenced by the generated provider.
type Service interface {
	GetData(context.Context, *GetDataPayload) (*GetDataResult, error)
}

// Client mirrors the Goa client referenced by the generated service executor.
type Client struct{}

// GetData mirrors the generated client endpoint wrapper.
func (c *Client) GetData(ctx context.Context, p *GetDataPayload) (*GetDataResult, error) {
	return &GetDataResult{OK: true}, nil
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "build", "-mod=mod",
		"./gen/catalog/toolsets/helpers", "./gen/catalog/agents/scribe/helpers"))
}

// TestGeneratedServerDataPackagesCompile compiles both generated call sites
// that encode server-only method result data: the registry provider and the
// agent-side service executor.
func TestGeneratedServerDataPackagesCompile(t *testing.T) {
	files := buildWithPrepareForGeneratedModule(t, testscenarios.ServiceToolsetBindSelfServerData())
	root := writeGeneratedModuleKeepingGen(t, files)

	// Stub the Goa service package that normal `goa gen` output supplies.
	writeGeneratedPackageTest(t, root, "gen/alpha/service_stub.go", `package alpha

import "context"

type Evidence struct {
	Kind string
}

type FindPayload struct {
	Ident string
}

type FindResult struct {
	Okay     bool
	Evidence []*Evidence
}

type Service interface {
	Find(context.Context, *FindPayload) (*FindResult, error)
}

type Client struct{}

func (c *Client) Find(ctx context.Context, p *FindPayload) (*FindResult, error) {
	return &FindResult{Okay: true, Evidence: []*Evidence{{Kind: "source"}}}, nil
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "build", "-mod=mod",
		"./gen/alpha/toolsets/lookup", "./gen/alpha/agents/scribe/lookup"))
}

// TestGeneratedBoundMetaInjectPackagesCompile is the bound half of the
// compile matrix: a BindTo tool injecting a meta-backed field (session_id),
// whose provider.go DOES declare the runtime.ToolCallMeta and call
// Inject<Tool>, and whose transforms deref the pointer injected field into
// the required method payload field. Locks the meta emission, the pointer
// assignment in inject.go, and the tool-payload -> method-payload transform
// as a compilable whole -- and, via the provider_exec_test.go file written
// into the generated module, EXECUTES the full generated chain
// (PayloadCodec.FromJSON -> InjectGetData -> InitGetDataMethodPayload
// pointer deref -> service call) with `go test`, asserting the bound method
// payload actually receives session metadata and immutable labels end to end.
func TestGeneratedBoundMetaInjectPackagesCompile(t *testing.T) {
	files := buildWithPrepareForGeneratedModule(t, testscenarios.InjectBoundMetaExample())
	root := writeGeneratedModuleKeepingGen(t, files)

	writeGeneratedPackageTest(t, root, "gen/catalog/service_stub.go", `package catalog

import "context"

// GetDataPayload mirrors the bound method payload emitted by Goa service codegen.
type GetDataPayload struct {
	HouseholdID string
	SessionID string
	Query     string
}

// GetDataResult mirrors the bound method result emitted by Goa service codegen.
type GetDataResult struct {
	OK bool
}

// Service mirrors the Goa service interface referenced by the generated provider.
type Service interface {
	GetData(context.Context, *GetDataPayload) (*GetDataResult, error)
}

// Client mirrors the Goa client referenced by the generated service executor.
type Client struct{}

// GetData mirrors the generated client endpoint wrapper.
func (c *Client) GetData(ctx context.Context, p *GetDataPayload) (*GetDataResult, error) {
	return &GetDataResult{OK: true}, nil
}
`)

	// Executing regression test for the registry provider path: compiled by
	// `go test` inside the generated module, it drives the generated
	// Provider.HandleToolCall and asserts the injected meta value survives
	// the Inject -> pointer -> transform-deref chain onto the bound method
	// payload. Text-level golden assertions cannot prove this chain RUNS;
	// only executing the generated code can.
	writeGeneratedPackageTest(t, root, "gen/catalog/toolsets/helpers/provider_exec_test.go", `package helpers

import (
	"context"
	"encoding/json"
	"testing"

	catalog "generated.local/gen/catalog"
	"goa.design/goa-ai/runtime/toolregistry"
)

// capturingService records the method payload the generated provider passes
// to the bound service method.
type capturingService struct {
	got           *catalog.GetDataPayload
	gotToolUseID  string
}

func (s *capturingService) GetData(ctx context.Context, p *catalog.GetDataPayload) (*catalog.GetDataResult, error) {
	s.got = p
	s.gotToolUseID, _ = toolregistry.ToolUseIDFromContext(ctx)
	return &catalog.GetDataResult{OK: true}, nil
}

// TestHandleToolCallInjectsContext executes the full generated chain:
// GetDataPayloadCodec.FromJSON decodes the wire payload (no session_id on
// the wire -- injected fields are hidden from the model), InjectGetData assigns
// the session metadata and immutable household label to pointer payload fields,
// and
// InitGetDataMethodPayload derefs it into the required method payload
// field the service receives.
func TestHandleToolCallInjectsContext(t *testing.T) {
	svc := &capturingService{}
	p := NewProvider(svc)
	ctx := toolregistry.WithToolUseID(context.Background(), "use-1")
	message := toolregistry.ToolCallMessage{
		ToolUseID: "use-1",
		Tool:      GetData,
		Payload:   []byte("{\"query\":\"weather\"}"),
		Meta: &toolregistry.ToolCallMeta{
			RunID:      "run-1",
			SessionID:  "sess-42",
			TurnID:     "turn-1",
			ToolCallID: "call-1",
			Labels:     map[string]string{"household_id": "household-7"},
		},
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal ToolCallMessage: %v", err)
	}
	var decoded toolregistry.ToolCallMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal ToolCallMessage: %v", err)
	}
	out, err := p.HandleToolCall(ctx, decoded)
	if err != nil {
		t.Fatalf("HandleToolCall returned error: %v", err)
	}
	if out.Error != nil {
		t.Fatalf("HandleToolCall returned tool error: %+v", out.Error)
	}
	if svc.got == nil {
		t.Fatal("bound service method was never called")
	}
	if svc.got.SessionID != "sess-42" {
		t.Fatalf("method payload SessionID = %q, want %q (injected from msg.Meta.SessionID)", svc.got.SessionID, "sess-42")
	}
	if svc.got.HouseholdID != "household-7" {
		t.Fatalf("method payload HouseholdID = %q, want %q (injected from msg.Meta.Labels)", svc.got.HouseholdID, "household-7")
	}
	if svc.got.Query != "weather" {
		t.Fatalf("method payload Query = %q, want %q", svc.got.Query, "weather")
	}
	if svc.gotToolUseID != "use-1" {
		t.Fatalf("method context ToolUseID = %q, want %q", svc.gotToolUseID, "use-1")
	}
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	// `go test` both compiles every listed package and runs the executing
	// provider-path test written above (the executor package has no test
	// files and is compile-checked only).
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "-count=1",
		"./gen/catalog/toolsets/helpers", "./gen/catalog/agents/scribe/helpers"))
}
