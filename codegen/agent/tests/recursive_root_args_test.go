// Package tests compiles a generated recursive root tool contract and proves
// its schema and codec accept the same nested input.
package tests

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
)

// TestGeneratedRecursiveRootArgsRegister writes and compiles the generated
// tool package, then constructs the model-facing definition from its schema,
// example, and codec.
func TestGeneratedRecursiveRootArgsRegister(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", testscenarios.RecursiveRootArgs())
	root := writeGeneratedModule(t, files)
	writeGeneratedPackageTest(t, root, "trees/toolsets/nodes/http/validate_stub.go", `package http

func ValidateWalkPayloadTransport(v *WalkPayloadTransport) error {
	return nil
}
`)
	writeGeneratedPackageTest(t, root, "trees/toolsets/nodes/recursive_contract_test.go", `package nodes

import (
	"testing"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestRecursiveToolDefinition(t *testing.T) {
	if _, err := model.NewToolDefinitionFromSpec(SpecWalk()); err != nil {
		t.Fatalf("NewToolDefinitionFromSpec returned error: %v", err)
	}
	payload, err := UnmarshalWalkPayload([]byte(`+"`"+`{"name":"root","next":{"name":"leaf"}}`+"`"+`))
	if err != nil {
		t.Fatalf("UnmarshalWalkPayload returned error: %v", err)
	}
	if payload.Next == nil || payload.Next.Name != "leaf" {
		t.Fatalf("unexpected recursive payload: %#v", payload)
	}
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "-count=1",
		"./trees/toolsets/nodes"))
}
