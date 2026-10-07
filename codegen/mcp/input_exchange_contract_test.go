// Package codegen tests generated additional-input exchanges through real HTTP
// requests. Typed service answers, exact state and endpoint authentication must
// survive every round without becoming model-visible arguments.
package codegen

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMCPGeneratedInputExchange compiles a native service and checks its input
// rounds through the framework HTTP caller and original configured endpoints.
func TestMCPGeneratedInputExchange(t *testing.T) {
	dir := t.TempDir()
	module := fmt.Sprintf(`module input-peer.local

go 1.27.0

require (
 github.com/modelcontextprotocol/go-sdk v1.8.0
 goa.design/goa-ai v0.0.0
 goa.design/goa/v3 v3.0.0
)

replace goa.design/goa-ai => %s
replace goa.design/goa/v3 => %s
`, filepath.ToSlash(testModuleDirectory(t, "goa.design/goa-ai")), filepath.ToSlash(testModuleDirectory(t, "goa.design/goa/v3")))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "design"), 0o700))
	for name, source := range map[string]string{
		"go.mod":           module,
		"design/design.go": inputExchangeDesign,
		"peer_test.go":     inputExchangeRuntime,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	for _, args := range [][]string{
		{"run", "-mod=mod", "goa.design/goa/v3/cmd/goa", "gen", "input-peer.local/design"},
		{"test", "-mod=mod", "-race", "-p=1", "./..."},
	} {
		started := time.Now()
		// #nosec G204 -- fixed generator and test commands target this synthetic module.
		command := exec.CommandContext(ctx, "go", args...)
		command.Dir = dir
		command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod -p=1")
		output, err := command.CombinedOutput()
		t.Logf("go %s took %s", args[0], time.Since(started).Round(time.Millisecond))
		require.NoError(t, err, string(output))
	}
}
