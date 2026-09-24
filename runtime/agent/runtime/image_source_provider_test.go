// Native-source preparation feeds existing provider encoders exactly the
// request produced by directly supplied ImageParts. These transports cannot
// perform network I/O and use only fixed synthetic credentials and responses.
package runtime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	openaioption "github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/features/model/openai"
	genpictures "goa.design/goa-ai/internal/testimage/gen/images/toolsets/pictures"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
)

const nativeImageBedrockResponses = "bedrock-responses"

func TestNativeImageSourceUsesExistingProviderEncoding(t *testing.T) {
	for _, name := range []string{"bedrock", "anthropic", "openai", nativeImageBedrockResponses} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			var client model.Client
			var transport *historyHTTPTransport
			if name == nativeImageBedrockResponses {
				const modelID = "fixture.openai.gpt-6-sol"
				transport = &historyHTTPTransport{response: `{"id":"response","model":"` + modelID + `","status":"completed","output":[{"id":"message","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"A picture.","annotations":[]}]}],"usage":{"input_tokens":4,"output_tokens":3,"total_tokens":7}}`}
				var err error
				client, err = openai.NewBedrock(ctx, "us-east-1", aws.CredentialsProviderFunc(historyTestCredentials),
					openai.Options{DefaultModel: modelID},
					openaioption.WithHTTPClient(&http.Client{Transport: transport}), openaioption.WithMaxRetries(0))
				require.NoError(t, err)
			} else {
				client, transport = historyEncodedClient(t, name, "")
			}
			image := historyPNGImage(t)
			request := &model.Request{Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{
				model.TextPart{Text: "Inspect this exact image."}, image, model.TextPart{Text: "Then compare."}, image,
			}}}}
			var directCount model.TokenCount
			if name == nativeImageBedrockResponses {
				var err error
				directCount, err = client.(model.TokenCounter).CountTokens(ctx, request)
				require.NoError(t, err)
				assert.False(t, directCount.Exact)
				assert.Empty(t, transport.bodies, "the estimate sends no HTTP")
			}
			_, err := client.Complete(ctx, request)
			require.NoError(t, err)
			source := model.ImageSourcePart{SourceKind: "test.image.v1", Data: rawjson.Message(`{"id":"chosen"}`)}
			request.Messages[0].Parts[1] = source
			request.Messages[0].Parts[3] = source
			reads := 0
			client, err = model.WithImageSourceResolver(client, func(_ context.Context, got model.ImageSourcePart, allowance int64) (model.ImagePart, error) {
				assert.Equal(t, source, got)
				assert.GreaterOrEqual(t, allowance, int64(len(image.Format)+len(image.Bytes)))
				reads++
				return image, nil
			})
			require.NoError(t, err)
			if name == nativeImageBedrockResponses {
				sourceCount, err := client.(model.TokenCounter).CountTokens(ctx, request)
				require.NoError(t, err)
				assert.Equal(t, directCount, sourceCount)
				assert.Len(t, transport.bodies, 1)
			}
			_, err = client.Complete(ctx, request)
			require.NoError(t, err)
			require.Len(t, transport.bodies, 2)
			assert.JSONEq(t, string(transport.bodies[0]), string(transport.bodies[1]))
			if name == nativeImageBedrockResponses {
				assert.Equal(t, 4, reads)
			} else {
				assert.Equal(t, 2, reads)
			}
			assert.NotContains(t, string(transport.bodies[1]), "source_kind")
		})
	}
}

// assertNativeImageStrictGeneratedExchange receives the messages recorded by
// real generated tool execution. The strict SDK route must preserve that exact
// call/result/image ordering and the generated tool's executable schema.
func assertNativeImageStrictGeneratedExchange(t *testing.T, ctx context.Context, messages []*model.Message, owner *imageFixtureOwner) {
	t.Helper()
	const modelID = "fixture.openai.gpt-6-sol"
	transport := &historyHTTPTransport{response: `{"id":"response","model":"` + modelID + `","status":"completed","output":[{"id":"message","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Compared.","annotations":[]}]}],"usage":{"input_tokens":4,"output_tokens":3,"total_tokens":7}}`}
	provider, err := openai.NewBedrockStrictProvider(ctx, "us-east-1", aws.CredentialsProviderFunc(historyTestCredentials),
		openai.Options{DefaultModel: modelID},
		openaioption.WithHTTPClient(&http.Client{Transport: transport}), openaioption.WithMaxRetries(0))
	require.NoError(t, err)
	base, err := model.NewClient(provider)
	require.NoError(t, err)
	definition, err := model.NewToolDefinitionFromSpec(genpictures.SpecView())
	require.NoError(t, err)
	nativeMessages := cloneImageFixtureMessages(t, messages)
	var resultIDs []string
	var imageIDs []string
	for _, message := range nativeMessages {
		for i, part := range message.Parts {
			switch part := part.(type) {
			case model.ToolResultPart:
				resultIDs = append(resultIDs, part.ToolUseID)
			case model.ImageSourcePart:
				descriptor, err := genpictures.ViewFixtureImageV1ServerDataCodec().FromJSON(part.Data)
				require.NoError(t, err)
				imageIDs = append(imageIDs, descriptor.ID)
				message.Parts[i] = model.ImagePart{Format: model.ImageFormatPNG, Bytes: slices.Clone(owner.bodies[descriptor.ID])}
			}
		}
	}
	require.Len(t, resultIDs, 2)
	require.Equal(t, []string{"photo-21", "photo-2"}, imageIDs)
	direct := &model.Request{Messages: nativeMessages, Tools: []*model.ToolDefinition{definition}}
	retained := &model.Request{Messages: messages, Tools: []*model.ToolDefinition{definition}}
	current := run.Context{SessionID: "holder"}
	client, err := model.WithImageSourceResolver(base, func(ctx context.Context, source model.ImageSourcePart, allowance int64) (model.ImagePart, error) {
		return owner.resolve(ctx, current, source, allowance)
	})
	require.NoError(t, err)
	directCount, err := base.CountTokens(ctx, direct)
	require.NoError(t, err)
	retainedCount, err := client.CountTokens(ctx, retained)
	require.NoError(t, err)
	assert.Equal(t, directCount, retainedCount)
	assert.False(t, retainedCount.Exact)
	assert.Empty(t, transport.bodies, "strict local counting sends no HTTP")
	_, err = base.Complete(ctx, direct)
	require.NoError(t, err)
	_, err = client.Complete(ctx, retained)
	require.NoError(t, err)
	require.Len(t, transport.bodies, 2)
	assert.JSONEq(t, string(transport.bodies[0]), string(transport.bodies[1]))
	var wire struct {
		Tools []struct {
			Name       string          `json:"name"`
			Strict     bool            `json:"strict"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tools"`
		Input []struct {
			Type    string `json:"type"`
			CallID  string `json:"call_id"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	require.NoError(t, json.Unmarshal(transport.bodies[1], &wire))
	require.Len(t, wire.Tools, 1)
	assert.Equal(t, "pictures_view", wire.Tools[0].Name)
	assert.True(t, wire.Tools[0].Strict)
	assert.Contains(t, string(wire.Tools[0].Parameters), `"additionalProperties":false`)
	var order []string
	for _, item := range wire.Input {
		switch item.Type {
		case "function_call", "function_call_output":
			order = append(order, item.Type+":"+item.CallID)
		default:
			for _, content := range item.Content {
				switch content.Type {
				case "input_image":
					order = append(order, "image")
				case "input_text":
					for _, id := range resultIDs {
						if content.Text == fmt.Sprintf("Image for tool result %q.", id) {
							order = append(order, "correlation:"+id)
						}
					}
				}
			}
		}
	}
	assert.Equal(t, []string{
		"function_call:" + resultIDs[0], "function_call:" + resultIDs[1],
		"function_call_output:" + resultIDs[0], "function_call_output:" + resultIDs[1],
		"correlation:" + resultIDs[0], "image", "correlation:" + resultIDs[1], "image",
	}, order)
	assert.NotContains(t, string(transport.bodies[1]), "source_kind")
	assert.NotContains(t, string(transport.bodies[1]), "sha256")
}
