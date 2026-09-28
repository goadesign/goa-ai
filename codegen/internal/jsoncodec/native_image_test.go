// These checks run the normal DSL-import generator, retaining both generated
// producer and shared owner packages for compiled interoperability assertions.
package jsoncodec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeneratedNativeImageOriginalContractInteroperability(t *testing.T) {
	design, err := os.ReadFile("testdata/native_image_design.go")
	require.NoError(t, err)
	assertions, err := os.ReadFile("testdata/native_image_interop_test.go")
	require.NoError(t, err)
	module, err := filepath.Abs("../../..")
	require.NoError(t, err)
	var ordinary map[string]json.RawMessage
	for _, configuration := range []struct {
		name            string
		native, reverse bool
	}{
		{name: "ordinary"},
		{name: "native", native: true},
		{name: "native_first", native: true, reverse: true},
	} {
		t.Run(configuration.name, func(t *testing.T) {
			root := compileModule(t, nil)
			runGo(t, root, "mod", "edit", "-require=goa.design/goa-ai@v0.0.0", "-replace=goa.design/goa-ai="+module)
			source := string(design)
			if !configuration.native {
				source = strings.Replace(source, "const includeNative = true", "const includeNative = false", 1)
			}
			if configuration.reverse {
				source = strings.Replace(source, "const reverseTools = false", "const reverseTools = true", 1)
			}
			directory := filepath.Join(root, "design")
			require.NoError(t, os.MkdirAll(directory, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(directory, "design.go"), []byte(source), 0o600)) // #nosec G703 -- compileModule creates a fresh directory; this generated filename is fixed.
			runGo(t, root, "mod", "tidy")
			runGo(t, root, "run", "goa.design/goa/v3/cmd/goa", "gen", "codec.local/design")

			artifact, err := os.ReadFile(filepath.Join(root, "gen/sample/agents/reader/specs/tool_schemas.json")) // #nosec G304 -- compileModule creates a fresh directory; this generated filename is fixed.
			require.NoError(t, err)
			var document struct {
				Tools []json.RawMessage `json:"tools"`
			}
			require.NoError(t, json.Unmarshal(artifact, &document))
			records := make(map[string]json.RawMessage)
			for _, record := range document.Tools {
				var identity struct {
					Name string `json:"id"`
				}
				require.NoError(t, json.Unmarshal(record, &identity))
				if strings.HasSuffix(identity.Name, ".ordinary") || strings.HasSuffix(identity.Name, ".graph") {
					records[identity.Name] = record
				}
			}
			require.Len(t, records, 2)
			if !configuration.native {
				ordinary = records
			} else {
				require.Equal(t, ordinary, records, "adding marked evidence must preserve ordinary artifact bytes")
				require.NoError(t, os.WriteFile(filepath.Join(root, "gen/sample/native_image_test.go"), assertions, 0o600)) // #nosec G703 -- compileModule creates a fresh directory; this generated filename is fixed.
			}

			tree := generatedGoTree(t, filepath.Join(root, "gen"))
			var unsupported, shared, transport strings.Builder
			for path, body := range tree {
				switch {
				case strings.HasPrefix(filepath.ToSlash(path), "unsupported/"):
					unsupported.WriteString(body)
				case strings.HasPrefix(filepath.ToSlash(path), "types/"):
					shared.WriteString(body)
				case filepath.ToSlash(path) == "sample/toolsets/pictures/http/types.go":
					transport.WriteString(body)
				}
			}
			for _, name := range []string{"DynamicOriginal", "CustomOriginal"} {
				require.Contains(t, unsupported.String(), "type "+name+" struct")
				require.NotContains(t, unsupported.String(), "func Encode"+name+"(")
				require.NotContains(t, unsupported.String(), "func Decode"+name+"(")
			}
			require.NotContains(t, unsupported.String(), "readStrictJSON")
			require.NotContains(t, unsupported.String(), `"goa.design/goa-ai/runtime/agent/tools"`)
			require.Contains(t, shared.String(), "func EncodeDetail(")
			require.Contains(t, shared.String(), "func EncodeImageSource(")
			require.Contains(t, shared.String(), "func DecodeGraph(")
			require.Contains(t, transport.String(), "NodeNativeImageTransport")
			runGo(t, root, "mod", "tidy")
			runGo(t, root, "test", "-count=1", "./gen/...")
		})
	}
}
