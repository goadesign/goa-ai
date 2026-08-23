// These tests run the documented quickstart command so generated and
// application-owned example code cannot drift apart unnoticed.
package orchestratorapi_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestQuickstartCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	command := exec.CommandContext(ctx, "go", "run", "./cmd/orchestrator")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run quickstart command: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Assistant:") {
		t.Fatalf("quickstart output has no assistant answer:\n%s", output)
	}
}
