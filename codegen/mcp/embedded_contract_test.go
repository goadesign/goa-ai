// These tests generate protocol types and check that extra JSON members survive
// strict codecs, HTTP response bodies, and protobuf without losing precision.
package codegen

import (
	"context"
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

//go:embed testdata/embedded/*.txt
var embeddedContractFiles embed.FS

func TestMCPEmbeddedJSONMembers(t *testing.T) {
	dir := t.TempDir()
	module := fmt.Sprintf("module open-fields.example\n\ngo 1.27.0\n\nrequire (\n goa.design/goa-ai v0.0.0\n goa.design/goa/v3 v3.0.0\n)\nreplace goa.design/goa-ai => %s\nreplace goa.design/goa/v3 => %s\n", filepath.ToSlash(testModuleDirectory(t, "goa.design/goa-ai")), filepath.ToSlash(testModuleDirectory(t, "goa.design/goa/v3")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "design"), 0o700))
	entries, err := embeddedContractFiles.ReadDir("testdata/embedded")
	require.NoError(t, err)
	for _, entry := range entries {
		source, err := embeddedContractFiles.ReadFile("testdata/embedded/" + entry.Name())
		require.NoError(t, err)
		name := strings.TrimSuffix(entry.Name(), ".txt")
		if name == "design_design.go" {
			name = "design/design.go"
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), source, 0o600))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	for _, args := range [][]string{
		{"run", "-mod=mod", "goa.design/goa/v3/cmd/goa", "gen", "open-fields.example/design"},
		{"test", "-mod=mod", "-count=1", "-v", "."},
	} {
		// #nosec G204 -- arguments are the fixed generator and test commands above, run in this test-created module.
		command := exec.CommandContext(ctx, "go", args...)
		command.Dir = dir
		command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
		output, err := command.CombinedOutput()
		t.Logf("go %s:\n%s", args[0], output)
		require.NoError(t, err)
	}
}
