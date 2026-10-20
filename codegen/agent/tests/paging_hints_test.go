// These tests decode query and page requests through generated execution codecs,
// then render their declared hints under the same public operation name.
package tests

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGeneratedPagingHints(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, pagingCompositionDesign)
	root := writeCompleteGeneratedModule(t, files)
	writeGeneratedPackageTest(t, root, "gen/consumer/agents/reader/paging_hints_test.go", `package reader

import (
	"testing"

	genrecords "generated.local/gen/consumer/toolsets/records"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/runtime/hints"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestQueryAndPageHints(t *testing.T) {
	registration := runtime.ToolsetRegistration{}
	require.NoError(t, installRecordsGeneratedHints(&registration))
	hints.RegisterCallHints(registration.CallHints)
	hints.RegisterResultHints(registration.ResultHints)
	for _, test := range []struct {
		name tools.Ident
		input, call, result string
	}{
		{genrecords.Same, "{\"selection\":{\"kind\":\"a\"}}", "Reading a", "Read a"},
		{genrecords.Same, "{\"selection\":{\"kind\":\"a\"},\"cursor\":\"next\"}", "Reading page next", "Read page next"},
		{genrecords.Different, "{\"selection\":{\"kind\":\"a\"}}", "Finding a", "Found a"},
		{genrecords.Different, "{\"cursor\":\"next\"}", "Finding page next", ""},
		{genrecords.Empty, "{}", "", ""},
		{genrecords.Empty, "{\"cursor\":\"next\"}", "Continuing next", "Continued next"},
	} {
		t.Run(string(test.name)+test.input, func(t *testing.T) {
			spec, ok := genrecords.Spec(test.name)
			require.True(t, ok)
			payload, err := spec.ExecutionPayloadCodec.FromJSON([]byte(test.input))
			require.NoError(t, err)
			call, present, err := hints.RenderCallHint(test.name, payload)
			assert.NoError(t, err)
			assert.Equal(t, test.call, call)
			assert.Equal(t, test.call != "", present)
			result, present, err := hints.RenderResultHint(test.name, struct { Args any }{payload})
			assert.NoError(t, err)
			assert.Equal(t, test.result, result)
			assert.Equal(t, test.result != "", present)
		})
	}
}
`)
	writeGeneratedPackageTest(t, root, "gen/native/agents/reader/agenttools/operations/paging_hints_test.go", `package operations

import (
	"testing"

	genoperations "generated.local/gen/native/agents/reader/exports/operations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/runtime/hints"
)

func TestNativeQueryAndPageHints(t *testing.T) {
	registration := runtime.ToolsetRegistration{}
	require.NoError(t, installGeneratedHints(&registration))
	hints.RegisterCallHints(registration.CallHints)
	hints.RegisterResultHints(registration.ResultHints)
	spec, exists := genoperations.Spec("operations.specs")
	require.True(t, exists)
	for _, test := range []struct { input, call, result string }{
		{"{\"selection\":{\"kind\":\"a\"}}", "Searching a", "Searched a"},
		{"{\"cursor\":\"next\"}", "Searching page next", "Searched page next"},
	} {
		payload, err := spec.ExecutionPayloadCodec.FromJSON([]byte(test.input))
		require.NoError(t, err)
		call, present, err := hints.RenderCallHint(spec.Name, payload)
		assert.NoError(t, err)
		assert.True(t, present)
		assert.Equal(t, test.call, call)
		result, present, err := hints.RenderResultHint(spec.Name, struct { Args any }{payload})
		assert.NoError(t, err)
		assert.True(t, present)
		assert.Equal(t, test.result, result)
	}
}
`)
	runGeneratedPackageTest(t, root, "./gen/...")
}

// TestExamplePagingHints checks that starter registrations install the same
// query/page selectors and keep private continuation names out of hint maps.
func TestExamplePagingHints(t *testing.T) {
	files := buildAndGenerateExample(t, pagingCompositionDesign)
	bootstrap := renderedFileContent(t, files, "internal/agents/consumer/bootstrap/bootstrap.go")
	assert.Contains(t, bootstrap, `"isPageDifferent": func(payload any) bool`)
	assert.Contains(t, bootstrap, `.DifferentNextPayload)`)
	assert.Contains(t, bootstrap, `{{if isPageDifferent .}}Finding page {{.Cursor}}{{else}}Finding {{.Selection.Kind}}{{end}}`)
	assert.NotContains(t, bootstrap, `"records.different_next":`)
}
