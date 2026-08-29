// This file compiles an external consumer against the generated route names
// and executes the second service's shared tool registration.
package tests

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// TestGeneratedSharedToolsetRegistrationNames catches generators that force
// application code to copy a local toolset route string.
func TestGeneratedSharedToolsetRegistrationNames(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.SharedToolsetConsumers())
	root := writeCompleteGeneratedModule(t, files)
	writeGeneratedPackageTest(t, root, "registrationnames/registration_names_test.go", `package registrationnames

import (
	"context"
	"testing"

	alpha "generated.local/gen/alpha/agents/alpha_worker"
	shared "generated.local/gen/alpha/toolsets/shared"
	beta "generated.local/gen/beta/agents/beta_worker"
	"goa.design/goa-ai/runtime/agent/planner"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
)

func TestGeneratedRoutes(t *testing.T) {
	if alpha.SharedToolsetName != "alpha.shared" {
		t.Fatalf("alpha route = %q", alpha.SharedToolsetName)
	}
	if beta.SharedToolsetName != "beta.shared" {
		t.Fatalf("beta route = %q", beta.SharedToolsetName)
	}
	alphaFingerprint, err := shared.SchemaFingerprint(alpha.SharedToolsetName)
	if err != nil {
		t.Fatal(err)
	}
	betaFingerprint, err := shared.SchemaFingerprint(beta.SharedToolsetName)
	if err != nil {
		t.Fatal(err)
	}
	if alphaFingerprint == betaFingerprint {
		t.Fatal("different registration routes have the same schema fingerprint")
	}
	if _, err := shared.SchemaFingerprint("unknown.shared"); err == nil {
		t.Fatal("unknown registration route was accepted")
	}

	executed := false
	executor := agentsruntime.ToolCallExecutorFunc(func(_ context.Context, _ *agentsruntime.ToolCallMeta, call *agentsruntime.ToolCall) (*agentsruntime.ToolExecutionResult, error) {
		executed = true
		return agentsruntime.Executed(&planner.ToolResult{
			Name:       call.Name,
			ToolCallID: call.ToolCallID,
			Result:     "pong",
		}), nil
	})
	runtime := agentsruntime.New()
	if err := beta.RegisterUsedToolsets(context.Background(), runtime, beta.WithSharedExecutor(executor)); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ExecuteToolActivity(context.Background(), &agentsruntime.ToolInput{
		RunID:       "run-1",
		AgentID:     "beta.beta_worker",
		ToolsetName: beta.SharedToolsetName,
		ToolName:    shared.Ping,
		ToolCallID:  "call-1",
		Payload:     []byte(`+"`"+`{"message":"ping"}`+"`"+`),
	}); err != nil {
		t.Fatal(err)
	}
	if !executed {
		t.Fatal("beta shared executor was not called")
	}
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runGeneratedGoTestCommand(t, root, exec.CommandContext(ctx, "go", "test", "-mod=mod", "./registrationnames"))
}
