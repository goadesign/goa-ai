// These tests preserve each tagged query's own optional fields when the provider
// emits strict-mode nulls. They use only synthetic public schema contracts.
package openai

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

const taggedOptionalSchema = `{"type":"object","properties":{"query":{"oneOf":[{"type":"object","properties":{"kind":{"const":"optional"},"value":{"type":"boolean"}},"required":["kind"],"additionalProperties":false},{"type":"object","properties":{"kind":{"enum":["required","required_alias"]},"value":{"type":"boolean"}},"required":["kind","value"],"additionalProperties":false}]}},"required":["query"],"additionalProperties":false}`

func TestStrictTaggedOmission(t *testing.T) {
	projection, err := compileStrictSchema(rawjson.Message(taggedOptionalSchema))
	require.NoError(t, err)
	for _, test := range []struct{ input, want string }{
		{`{"query":{"kind":"optional","value":null}}`, `{"query":{"kind":"optional"}}`},
		{`{"query":{"kind":"optional","value":false}}`, `{"query":{"kind":"optional","value":false}}`},
		{`{"query":{"kind":"required","value":null}}`, `{"query":{"kind":"required","value":null}}`},
		{`{"query":{"kind":"required_alias","value":false}}`, `{"query":{"kind":"required_alias","value":false}}`},
		{`{"query":{"kind":"unknown","value":null}}`, `{"query":{"kind":"unknown","value":null}}`},
		{`{"query":{"value":null}}`, `{"query":{"value":null}}`},
	} {
		t.Run(test.input, func(t *testing.T) {
			actual, err := projection.canonicalize([]byte(test.input))
			require.NoError(t, err)
			assert.JSONEq(t, test.want, string(actual))
		})
	}
}

func TestBedrockStrictSDKContract(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, valid := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/valid=%t", streaming, valid), func(t *testing.T) {
				arguments := `{"query":{"kind":"optional","value":null}}`
				if !valid {
					arguments = `{"query":{"kind":"unknown","value":null}}`
				}
				calls := 0
				provider, err := NewBedrockStrictProvider(t.Context(), "us-west-2", credentials.NewStaticCredentialsProvider("test", "test", "test"), Options{DefaultModel: bedrockTestModel}, option.WithMaxRetries(0), option.WithHTTPClient(&http.Client{Transport: bedrockRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					assert.Contains(t, string(body), `"strict":true`)
					assert.Contains(t, string(body), `"store":false`)
					assert.Contains(t, string(body), `"truncation":"disabled"`)
					if streaming {
						return bedrockToolStreamResponse(t, "query_read", arguments), nil
					}
					return bedrockJSONResponse(bedrockToolResponse(t, "", "query_read", arguments)), nil
				})}))
				require.NoError(t, err)
				require.Implements(t, (*model.TokenCounter)(nil), provider)
				client, err := model.NewClient(provider)
				require.NoError(t, err)
				request := &model.Request{Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Read the selected query."}}}}, Tools: []*model.ToolDefinition{{Name: "query.read", Description: "Read a query.", Input: mustOpenAIToolInput(rawjson.Message(taggedOptionalSchema))}}}
				var response *model.Response
				if streaming {
					stream, streamErr := client.Stream(t.Context(), request)
					require.NoError(t, streamErr)
					defer func() { assert.NoError(t, stream.Close()) }()
					for err == nil {
						_, err = stream.Recv()
					}
					if errors.Is(err, io.EOF) {
						err = nil
					}
					response = stream.Response()
				} else {
					response, err = client.Complete(t.Context(), request)
				}
				if valid {
					require.NoError(t, err)
					require.Len(t, response.ToolCalls(), 1)
					assert.JSONEq(t, `{"query":{"kind":"optional"}}`, string(response.ToolCalls()[0].Payload))
				} else {
					var invalid *model.OutputValidationError
					require.ErrorAs(t, err, &invalid)
				}
				assert.Equal(t, 1, calls)
			})
		}
	}
}

func TestStrictEmptyObjectsRequireEmptyArray(t *testing.T) {
	for _, schema := range []string{"", `{"type":"object","properties":{"value":{"type":"object","additionalProperties":false}},"additionalProperties":false}`} {
		projected, err := projectStrictSchema(rawjson.Message(schema))
		require.NoError(t, err)
		if schema == "" {
			assert.Equal(t, []any{}, projected["required"])
		} else {
			properties := projected["properties"].(map[string]any)
			branches := properties["value"].(map[string]any)["anyOf"].([]any)
			assert.Equal(t, []any{}, branches[0].(map[string]any)["required"])
		}
	}
}

// A pattern whose alternatives disagree on null cannot preserve omission in
// strict mode. Complete-schema generation keeps its original null meaning.
func TestPublishedPatternOmissionContract(t *testing.T) {
	const schema = `{"type":"object","additionalProperties":false,"patternProperties":{"^v$":{"anyOf":[{"type":"object","additionalProperties":false,"properties":{"x":{"type":"string"}}},{"type":"object","additionalProperties":false,"properties":{"x":{"type":["string","null"]}},"required":["x"]}]}}}`
	const arguments = `{"v":{"x":null}}`
	for _, bedrock := range []bool{false, true} {
		t.Run(fmt.Sprintf("bedrock=%t", bedrock), func(t *testing.T) {
			calls := 0
			client := newReplayTestClient(t, bedrock, Options{}, bedrockRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				assert.Contains(t, string(body), fmt.Sprintf(`"strict":%t`, !bedrock))
				return bedrockJSONResponse(bedrockToolResponse(t, "", "records_inspect", arguments)), nil
			}))
			request := &model.Request{Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Inspect records."}}}}, Tools: []*model.ToolDefinition{{Name: "records.inspect", Description: "Inspect records.", Input: mustOpenAIToolInput(rawjson.Message(schema))}}}
			response, err := client.Complete(t.Context(), request)
			if !bedrock {
				require.ErrorContains(t, err, "conflicting null handling")
				assert.Zero(t, calls)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 1, calls)
			assert.True(t, bytes.Equal([]byte(arguments), response.ToolCalls()[0].Payload), "preserve original argument bytes")
		})
	}
}
