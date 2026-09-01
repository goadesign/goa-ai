// Package tests verifies that each generated codec file reserves only the
// fixed package names used by that file.
package tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestGoldenCodecImportNameCollisions(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.CodecImportNameCollisions())
	indexed := renderedFileContent(t, files, "gen/alpha/toolsets/indexed/codecs.go")
	scalar := renderedFileContent(t, files, "gen/alpha/toolsets/scalar/types.go")
	customTransport := renderedFileContent(t, files, "gen/alpha/toolsets/custom/http/types.go")
	customValidation := renderedFileContent(t, files, "gen/alpha/toolsets/custom/http/validate.go")
	union := renderedFileContent(t, files, "gen/alpha/toolsets/union/unions.go")
	sharedTypes := renderedFileContent(t, files, "gen/alpha/toolsets/shared/types.go")
	completionTypes := renderedFileContent(t, files, "gen/alpha/completions/types.go")
	completionCodecs := renderedFileContent(t, files, "gen/alpha/completions/codecs.go")

	require.Contains(t, indexed, `"strconv"`)
	require.Contains(t, indexed, `strconv2 "generated.local/gen/strconv"`)
	require.Contains(t, indexed, "strconv.Itoa(index)")
	require.Contains(t, indexed, "strconv2.Token")
	require.Contains(t, scalar, `strconv "generated.local/gen/strconv"`)
	require.NotContains(t, scalar, `strconv2`)
	require.NotContains(t, scalar, "\n\t\"strconv\"")
	require.Contains(t, customTransport, `goa2 "generated.local/custom/goa"`)
	require.Contains(t, customTransport, "*goa2.Token")
	require.Contains(t, customValidation, `goa "goa.design/goa/v3/pkg"`)
	require.Contains(t, union, `"errors"`)
	require.Contains(t, union, `errors_ "generated.local/gen/errors"`)
	require.Contains(t, union, "*errors_.RelocatedBranch")
	require.Contains(t, sharedTypes, `alpha "generated.local/custom/shared"`)
	require.Contains(t, sharedTypes, "alpha.Value")
	require.Contains(t, sharedTypes, "alpha.Other")
	require.NotContains(t, sharedTypes, "beta.Other")
	require.Contains(t, completionTypes, `errors "generated.local/custom/errors"`)
	require.NotContains(t, completionTypes, `errors2`)
	require.NotContains(t, completionCodecs, "\n\t\"errors\"")

	assertGoldenGo(t, "codec_import_name_collisions", "indexed_codecs.go.golden", indexed)
	assertGoldenGo(t, "codec_import_name_collisions", "scalar_types.go.golden", scalar)
	assertGoldenGo(t, "codec_import_name_collisions", "custom_transport_types.go.golden", customTransport)
	assertGoldenGo(t, "codec_import_name_collisions", "union_types.go.golden", union)
	assertGoldenGo(t, "codec_import_name_collisions", "shared_types.go.golden", sharedTypes)
	assertGoldenGo(t, "codec_import_name_collisions", "completion_types.go.golden", completionTypes)
	assertGoldenGo(t, "codec_import_name_collisions", "completion_codecs.go.golden", completionCodecs)

	root := writeCompleteGeneratedModule(t, files)
	customPackage := filepath.Join(root, "custom", "goa")
	require.NoError(t, os.MkdirAll(customPackage, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(customPackage, "token.go"),
		[]byte("package goa\n\ntype Token string\n"),
		0o600,
	))
	customErrorsPackage := filepath.Join(root, "custom", "errors")
	require.NoError(t, os.MkdirAll(customErrorsPackage, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(customErrorsPackage, "value.go"),
		[]byte("package errors\n\ntype Value any\n"),
		0o600,
	))
	customSharedPackage := filepath.Join(root, "custom", "shared")
	require.NoError(t, os.MkdirAll(customSharedPackage, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(customSharedPackage, "values.go"),
		[]byte("package shared\n\ntype Value string\ntype Other string\n"),
		0o600,
	))
	runGeneratedPackageTest(t, root, "./gen/alpha/toolsets/...")
	runGeneratedPackageTest(t, root, "./gen/alpha/completions")
}
