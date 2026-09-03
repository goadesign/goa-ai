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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
	gcodegen "goa.design/goa/v3/codegen"
)

// buildWithPrepareAndPkg generates against the same module path used by the
// compile tests. Realistic genpkgs end in "/gen" (the goa CLI always passes
// "<module>/gen"), which keeps the generator's two service import forms --
// shared.JoinImportPath (inserts /gen/) and plain path.Join -- identical.
func buildWithPrepareAndPkg(t *testing.T, design func()) []*gcodegen.File {
	t.Helper()
	return testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", design)
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

// TestGeneratedMixedInjectPackagesCompile generates the mixed
// bound/unbound Inject() scenario (a non-injecting BindTo tool sharing a
// toolset with a label-injecting unbound tool) into a temp module and
// compiles both generated packages that carry injection code: the toolset
// specs package (codecs, inject.go, transforms, provider.go) and the
// agent-side service executor package. Only the Goa-core service package
// (which agent codegen does not emit) is stubbed; every agent-generated
// file, including http/validate.go, is compiled verbatim.
func TestGeneratedMixedInjectPackagesCompile(t *testing.T) {
	files := buildWithPrepareAndPkg(t, testscenarios.InjectMixedBoundUnboundExample())
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
	files := buildWithPrepareAndPkg(t, testscenarios.ServiceToolsetBindSelfServerData())
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
// Inject<Tool>, and whose transforms copy the injected field into the required
// method payload field. Locks the metadata emission, assignment in inject.go,
// and the tool-payload to method-payload transform
// as a compilable whole -- and, via the provider_exec_test.go file written
// into the generated module, EXECUTES the full generated chain
// (PayloadCodec.FromJSON -> InjectGetData -> InitGetDataMethodPayload ->
// service call) with `go test`, asserting the bound method
// payload actually receives session metadata and immutable labels end to end.
func TestGeneratedBoundMetaInjectPackagesCompile(t *testing.T) {
	files := buildWithPrepareAndPkg(t, testscenarios.InjectBoundMetaExample())
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
	// Provider.HandleToolCall and asserts the injected metadata value survives
	// injection and conversion into the bound method
	// payload. Text-level golden assertions cannot prove this chain RUNS;
	// only executing the generated code can.
	writeGeneratedPackageTest(t, root, "gen/catalog/toolsets/helpers/provider_exec_test.go", `package helpers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	catalog "generated.local/gen/catalog"
	"goa.design/goa-ai/runtime/agent/runtime"
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
// the session metadata and immutable household label to required payload fields,
// and InitGetDataMethodPayload copies them into the required method payload
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
			SessionID:  "session-42",
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
	if svc.got.SessionID != "session-42" {
		t.Fatalf("method payload SessionID = %q, want %q (injected from msg.Meta.SessionID)", svc.got.SessionID, "session-42")
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

// TestInjectGetDataValidatesEverySource passes invalid metadata and labels to
// the generated function and verifies that both values are rejected.
func TestInjectGetDataValidatesEverySource(t *testing.T) {
	tests := []struct {
		name      string
		meta      runtime.ToolCallMeta
		labels    map[string]string
		wantField string
	}{
		{
			name:      "call metadata",
			meta:      runtime.ToolCallMeta{SessionID: "short"},
			labels:    map[string]string{"household_id": "household-7"},
			wantField: "session_id",
		},
		{
			name:      "run label",
			meta:      runtime.ToolCallMeta{SessionID: "session-42"},
			labels:    map[string]string{"household_id": "short"},
			wantField: "household_id",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := InjectGetData(&GetDataPayload{}, test.meta, test.labels)
			if err == nil || !strings.Contains(err.Error(), test.wantField) {
				t.Fatalf("InjectGetData error = %v, want %s validation error", err, test.wantField)
			}
		})
	}

	payload := &GetDataPayload{}
	err := InjectGetData(
		payload,
		runtime.ToolCallMeta{SessionID: "session-42"},
		map[string]string{"household_id": "household-7"},
	)
	if err != nil {
		t.Fatalf("InjectGetData returned error for valid values: %v", err)
	}
	if payload.SessionID != "session-42" || payload.HouseholdID != "household-7" {
		t.Fatalf("injected payload = %+v, want session-42 and household-7", payload)
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

// TestGeneratedLocatedStringInjectCompiles verifies that generated injection
// validates and assigns a named Goa String stored in another package.
func TestGeneratedLocatedStringInjectCompiles(t *testing.T) {
	files := buildWithPrepareAndPkg(t, testscenarios.InjectLocatedStringExample())
	root := writeGeneratedModuleKeepingGen(t, files)

	writeGeneratedPackageTest(t, root, "gen/types/runtime_session_id.go", `package types

type RuntimeSessionID string
`)
	writeGeneratedPackageTest(t, root, "gen/catalog/service_stub.go", `package catalog

import (
	"context"

	"generated.local/gen/types"
)

type LookupPayload struct {
	SessionID types.RuntimeSessionID
}

type Service interface {
	Lookup(context.Context, *LookupPayload) (string, error)
}

type Client struct{}

func (c *Client) Lookup(context.Context, *LookupPayload) (string, error) {
	return "ok", nil
}
`)
	writeGeneratedPackageTest(t, root, "gen/catalog/toolsets/helpers/inject_exec_test.go", `package helpers

import (
	"strings"
	"testing"

	"goa.design/goa-ai/runtime/agent/runtime"
)

// TestInjectLookupValidatesSessionID passes invalid and valid call metadata to
// the generated function and verifies validation happens before assignment.
func TestInjectLookupValidatesSessionID(t *testing.T) {
	payload := &LookupPayload{}
	err := InjectLookup(payload, runtime.ToolCallMeta{SessionID: "short"}, nil)
	if err == nil || !strings.Contains(err.Error(), "sessionId") {
		t.Fatalf("InjectLookup error = %v, want sessionId validation error", err)
	}

	payload = &LookupPayload{}
	err = InjectLookup(payload, runtime.ToolCallMeta{SessionID: "session-42"}, nil)
	if err != nil {
		t.Fatalf("InjectLookup returned error for valid session ID: %v", err)
	}
	if payload.SessionID != "session-42" {
		t.Fatalf("SessionID = %q, want session-42", payload.SessionID)
	}
}
`)

	inject := fileContent(t, files, "gen/catalog/toolsets/helpers/inject.go")
	require.Contains(t, inject, "p.SessionID = types.RuntimeSessionID(v)")
	require.Contains(t, inject, `if utf8.RuneCountInString(v) < 8`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "-count=1",
		"./gen/catalog/toolsets/helpers", "./gen/catalog/agents/scribe/helpers"))
}

// TestGeneratedInjectImportCollisionsCompile verifies that field packages and
// validation packages use the same final import names.
func TestGeneratedInjectImportCollisionsCompile(t *testing.T) {
	files := buildWithPrepareAndPkg(t, testscenarios.InjectImportCollisionExample())
	root := writeGeneratedModuleKeepingGen(t, files)

	writeGeneratedPackageTest(t, root, "gen/utf8/runtime_session_id.go", `package utf8

type RuntimeSessionID string
`)
	writeGeneratedPackageTest(t, root, "gen/goa/run_id.go", `package goa

type RunID string
`)
	writeGeneratedPackageTest(t, root, "gen/fmt/tenant_id.go", `package fmt

type TenantID string
`)
	writeGeneratedPackageTest(t, root, "gen/catalog/service_stub.go", `package catalog

import (
	"context"

	genfmt "generated.local/gen/fmt"
	gengoa "generated.local/gen/goa"
	genutf8 "generated.local/gen/utf8"
)

type LookupPayload struct {
	SessionID      genutf8.RuntimeSessionID
	RunID          gengoa.RunID
	OrganizationID genfmt.TenantID
}

type Service interface {
	Lookup(context.Context, *LookupPayload) (string, error)
}

type Client struct{}

func (c *Client) Lookup(context.Context, *LookupPayload) (string, error) {
	return "ok", nil
}
`)
	writeGeneratedPackageTest(t, root, "gen/catalog/toolsets/helpers/inject_collision_test.go", `package helpers

import (
	"testing"

	"goa.design/goa-ai/runtime/agent/runtime"
)

func TestInjectLookupUsesCollidingPackages(t *testing.T) {
	payload := &LookupPayload{}
	err := InjectLookup(
		payload,
		runtime.ToolCallMeta{SessionID: "session-42", RunID: "run-42"},
		map[string]string{"tenant_id": "tenant-7"},
	)
	if err != nil {
		t.Fatalf("InjectLookup returned error for valid values: %v", err)
	}
	if payload.SessionID != "session-42" || payload.RunID != "run-42" || payload.OrganizationID != "tenant-7" {
		t.Fatalf("injected payload = %+v, want session-42, run-42, and tenant-7", payload)
	}
}
`)
	inject := fileContent(t, files, "gen/catalog/toolsets/helpers/inject.go")
	for _, path := range []string{
		`"fmt"`,
		`"generated.local/gen/fmt"`,
		`"generated.local/gen/goa"`,
		`"generated.local/gen/utf8"`,
		`"goa.design/goa/v3/pkg"`,
		`"unicode/utf8"`,
	} {
		require.Contains(t, inject, path)
	}
	require.Contains(t, inject, "p.OrganizationID =")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "-count=1",
		"./gen/catalog/toolsets/helpers", "./gen/catalog/agents/scribe/helpers"))
}

// TestGoaAndGoaAIDSLDotImportsCompile evaluates a design that calls both DSLs
// directly. Go compilation fails before this test runs if the packages export
// the same name.
func TestGoaAndGoaAIDSLDotImportsCompile(t *testing.T) {
	testhelpers.RunDesign(t, testscenarios.InjectReusableExportExample())
}

// TestGeneratedReusableInjectPackagesCompile verifies that a shared toolset
// exported by one service and used by another produces one complete contract.
// The generated provider test also proves a hidden session ID reaches the
// bound service method.
func TestGeneratedReusableInjectPackagesCompile(t *testing.T) {
	files := buildWithPrepareAndPkg(t, testscenarios.InjectReusableExportExample())
	root := writeGeneratedModuleKeepingGen(t, files)

	writeGeneratedPackageTest(t, root, "gen/atlas/service_stub.go", `package atlas

import "context"

type InheritedPayload struct {
	SessionID string
	Query string
}

type ExplicitArgs struct {
	SessionID string
	Query string
}

type Service interface {
	Inherited(context.Context, *InheritedPayload) (string, error)
	Explicit(context.Context, *ExplicitArgs) (string, error)
}

type Client struct{}

func (c *Client) Inherited(context.Context, *InheritedPayload) (string, error) {
	return "ok", nil
}

func (c *Client) Explicit(context.Context, *ExplicitArgs) (string, error) {
	return "ok", nil
}
`)

	writeGeneratedPackageTest(t, root, "gen/atlas/toolsets/helpers/provider_exec_test.go", `package helpers

import (
	"context"
	"testing"

	atlas "generated.local/gen/atlas"
	"goa.design/goa-ai/runtime/toolregistry"
)

type capturingReusableService struct {
	got *atlas.InheritedPayload
}

func (s *capturingReusableService) Inherited(_ context.Context, p *atlas.InheritedPayload) (string, error) {
	s.got = p
	return "ok", nil
}

func (s *capturingReusableService) Explicit(_ context.Context, _ *atlas.ExplicitArgs) (string, error) {
	return "ok", nil
}

func TestReusableProviderInjectsSession(t *testing.T) {
	svc := &capturingReusableService{}
	provider := NewProvider(svc)
	ctx := toolregistry.WithToolUseID(context.Background(), "use-1")
	result, err := provider.HandleToolCall(ctx, toolregistry.ToolCallMessage{
		ToolUseID: "use-1",
		Tool: Inherited,
		Payload: []byte("{\"query\":\"weather\"}"),
		Meta: &toolregistry.ToolCallMeta{SessionID: "sess-42"},
	})
	if err != nil {
		t.Fatalf("HandleToolCall returned error: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("HandleToolCall returned tool error: %+v", result.Error)
	}
	if svc.got == nil {
		t.Fatal("bound service method was never called")
	}
	if svc.got.SessionID != "sess-42" {
		t.Fatalf("SessionID = %q, want sess-42", svc.got.SessionID)
	}
	if svc.got.Query != "weather" {
		t.Fatalf("Query = %q, want weather", svc.got.Query)
	}
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "-count=1",
		"./gen/atlas/toolsets/helpers", "./gen/chat/agents/assistant/helpers"))
}
