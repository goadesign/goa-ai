// These tests pass generated provider result data and separate Bounds containing
// counts, truncation, and paging information through the portable consumer codec,
// then check the final result published by the runtime.
package tests

import (
	"testing"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestGeneratedBoundedProviderPortableResult(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.ServiceToolsetBindSelfBoundedResultExactTotal())
	root := writeCompleteGeneratedModule(t, files)
	writeGeneratedPackageTest(t, root, "gen/alpha/toolsets/lookup/portable_result_test.go", `package lookup_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genalpha "generated.local/gen/alpha"
	genlookup "generated.local/gen/alpha/toolsets/lookup"
	"goa.design/goa-ai/runtime/agent/rawjson"
	airuntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/goa-ai/runtime/toolregistry/contract"
	"goa.design/goa-ai/runtime/toolregistry/schema"
)

type recordsService struct {
	result *genalpha.SearchResult2
}

func (s *recordsService) Search(context.Context, *genalpha.SearchPayload2) (*genalpha.SearchResult2, error) {
	return s.result, nil
}

func TestProviderResultReachesPortableConsumer(t *testing.T) {
	cursor := "page-2"
	for _, test := range []struct {
		name string
		truncated bool
		cursor *string
	}{
		{name: "complete"},
		{name: "paged", truncated: true, cursor: &cursor},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := genlookup.NewProvider(&recordsService{result: &genalpha.SearchResult2{
				Results: []string{"record_2"}, Returned: 1, Total: 2,
				Truncated: test.truncated, NextCursor: test.cursor,
			}})
			message, err := provider.HandleToolCall(toolregistry.WithToolUseID(t.Context(), "bounded-call"), toolregistry.ToolCallMessage{
				RegistrationToken: strings.Repeat("a", 64), ToolUseID: "bounded-call",
				Tool: genlookup.Search, Payload: []byte("{\"query\":\"records\"}"),
				Meta: &toolregistry.ToolCallMeta{RunID: "run-1", ToolCallID: "call-1"},
			})
			require.NoError(t, err)
			require.Nil(t, message.Error)
			require.NotNil(t, message.Bounds)
			assert.JSONEq(t, "{\"results\":[\"record_2\"]}", string(message.Result))
			assert.Equal(t, 1, message.Bounds.Returned)
			require.NotNil(t, message.Bounds.Total)
			assert.Equal(t, 2, *message.Bounds.Total)
			assert.Equal(t, test.cursor, message.Bounds.NextCursor)

			body, err := toolregistry.EncodeToolResultMessage(message)
			require.NoError(t, err)
			transport, err := toolregistry.DecodeToolResultMessage(body)
			require.NoError(t, err)
			require.NoError(t, toolregistry.ValidateToolResultMessage(transport))
			declarations := genlookup.ToolSchemas()
			require.Len(t, declarations, 1)
			portable, err := contract.Compile(declarations[0])
			require.NoError(t, err)
			native := genlookup.SpecSearch()
			for _, spec := range []struct {
				name string
				decode func([]byte) (any, error)
			}{
				{name: "native", decode: native.Result.Codec.FromJSON},
				{name: "portable", decode: portable.Result.Codec.FromJSON},
			} {
				_, err := spec.decode(transport.Result)
				require.NoError(t, err, spec.name)
			}
			require.NoError(t, airuntime.ValidateSuccessfulToolResult(portable, rawjson.Message(transport.Result), transport.Bounds))
			value, err := portable.Result.Codec.FromJSON(transport.Result)
			require.NoError(t, err)
			final, err := airuntime.EncodeCanonicalToolResult(portable, value, transport.Bounds)
			require.NoError(t, err)
			public, err := schema.Codec(declarations[0].ResultSchema)
			require.NoError(t, err)
			_, err = public.FromJSON(transport.Result)
			assert.Error(t, err, "public output still requires counts and truncation")
			decoded, err := public.FromJSON(final)
			require.NoError(t, err)
			fields := decoded.(map[string]any)
			assert.Equal(t, json.Number("1"), fields["returned"])
			assert.Equal(t, json.Number("2"), fields["total"])
			assert.Equal(t, test.truncated, fields["truncated"])
			_, err = portable.Result.Codec.FromJSON(final)
			assert.NoError(t, err, "public clients also decode finalized JSON")
			nativeValue, err := native.Result.Codec.FromJSON(transport.Result)
			require.NoError(t, err)
			nativeFinal, err := airuntime.EncodeCanonicalToolResult(native, nativeValue, transport.Bounds)
			require.NoError(t, err)
			assert.JSONEq(t, string(nativeFinal), string(final))
			assert.Equal(t, declarations[0].ResultSchema, []byte(portable.Result.Schema))
		})
	}
}
`)
	runGeneratedPackageTest(t, root, "./gen/alpha/toolsets/lookup")
}
