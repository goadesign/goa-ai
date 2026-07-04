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
	codegen "goa.design/goa-ai/codegen/agent"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
	gcodegen "goa.design/goa/v3/codegen"
)

// buildWithPrepareAndPkg mirrors buildWithPrepare but generates against an
// explicit genpkg. Realistic genpkgs end in "/gen" (the goa CLI always passes
// "<module>/gen"), which keeps the generator's two service import forms --
// shared.JoinImportPath (inserts /gen/) and plain path.Join -- identical.
func buildWithPrepareAndPkg(t *testing.T, genpkg string, design func()) []*gcodegen.File {
	t.Helper()
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
func writeGeneratedModuleKeepingGen(t *testing.T, modulePath string, files []*gcodegen.File) string {
	t.Helper()
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../../..")
	require.NoError(t, err)
	goMod := "module " + modulePath + "\n\ngo 1.24\n\nrequire goa.design/goa-ai v0.0.0\n\nreplace goa.design/goa-ai => " + filepath.ToSlash(repoRoot) + "\n"
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
	files := buildWithPrepareAndPkg(t, "generated.local/gen", testscenarios.InjectMixedBoundUnboundExample())
	root := writeGeneratedModuleKeepingGen(t, "generated.local", files)

	// Stub the Goa-core service package (emitted by `goa gen`'s service
	// codegen, not by the agent generator): the generated provider,
	// transforms, and service executor import generated.local/gen/atlas for
	// the Service interface, Client, and method payload/result types.
	writeGeneratedPackageTest(t, root, "gen/atlas/service_stub.go", `package atlas

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
		"./gen/atlas/toolsets/helpers", "./gen/atlas/agents/scribe/helpers"))
}

// TestGeneratedBoundMetaInjectPackagesCompile is the bound half of the
// compile matrix: a BindTo tool injecting a meta-backed field (session_id),
// whose provider.go DOES declare the runtime.ToolCallMeta and call
// Inject<Tool>, and whose transforms deref the pointer injected field into
// the required method payload field. Locks the meta emission, the pointer
// assignment in inject.go, and the tool-payload -> method-payload transform
// as a compilable whole.
func TestGeneratedBoundMetaInjectPackagesCompile(t *testing.T) {
	files := buildWithPrepareAndPkg(t, "generated.local/gen", testscenarios.InjectBoundMetaExample())
	root := writeGeneratedModuleKeepingGen(t, "generated.local", files)

	writeGeneratedPackageTest(t, root, "gen/atlas/service_stub.go", `package atlas

import "context"

// GetDataPayload mirrors the bound method payload emitted by Goa service codegen.
type GetDataPayload struct {
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "build", "-mod=mod",
		"./gen/atlas/toolsets/helpers", "./gen/atlas/agents/scribe/helpers"))
}
