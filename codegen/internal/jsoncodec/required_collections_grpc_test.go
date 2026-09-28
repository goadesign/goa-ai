// These tests generate a normal gRPC service and a separate forced JSON
// envelope. Unsupported sibling representations must leave its codec intact.
package jsoncodec

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGeneratedGRPCCollectionsReachForcedOriginalCodec checks actual protobuf
// bytes, generated service construction, and the complete original JSON codec.
func TestGeneratedGRPCCollectionsReachForcedOriginalCodec(t *testing.T) {
	root := compileModule(t, nil)
	module, err := filepath.Abs("../../..")
	require.NoError(t, err)
	runGo(t, root, "mod", "edit", "-require=goa.design/goa-ai@v0.0.0", "-replace=goa.design/goa-ai="+module)
	designDirectory := filepath.Join(root, "design")
	require.NoError(t, os.MkdirAll(designDirectory, 0o700)) // #nosec G703 -- root is this test's private module.
	design, err := os.ReadFile("testdata/required_collections_grpc_design.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(designDirectory, "design.go"), design, 0o600)) // #nosec G703 -- fixed path in the private module.
	runGo(t, root, "mod", "tidy")
	runGo(t, root, "run", "goa.design/goa/v3/cmd/goa", "gen", "codec.local/design")

	source, err := os.ReadFile(filepath.Join(root, "gen/collections/service.go")) // #nosec G304 -- fixed generated package in the private module.
	require.NoError(t, err)
	for _, name := range []string{"Envelope", "Record", "Item", "Node"} {
		require.Contains(t, string(source), "func Encode"+name+"(")
		require.Contains(t, string(source), "func Decode"+name+"(")
	}
	for _, name := range []string{"AnySibling", "CustomSibling"} {
		require.Contains(t, string(source), "type "+name+" struct")
		require.NotContains(t, string(source), "func Encode"+name+"(")
		require.NotContains(t, string(source), "func Decode"+name+"(")
	}
	fixture, err := os.ReadFile("testdata/required_collections_grpc_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "roundtrip_test.go"), fixture, 0o600)) // #nosec G703 -- fixed test path in the private module.
	runGo(t, root, "mod", "tidy")
	runGo(t, root, "test", "-count=1", "-v", ".")
}
