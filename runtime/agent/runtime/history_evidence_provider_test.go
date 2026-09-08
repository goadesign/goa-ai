package runtime_test

// These tests send Compress requests through validated clients and real provider
// encoders. A local HTTP transport records the encoded bodies and returns fixed
// responses; no provider endpoint, model, or token-count service is contacted.
import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	openaisdk "github.com/openai/openai-go/v3"
	openaioption "github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/features/model/anthropic"
	"goa.design/goa-ai/features/model/bedrock"
	"goa.design/goa-ai/features/model/openai"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
)

type (
	// historyHTTPTransport has no network transport to delegate to. Unexpected
	// requests fail locally rather than attempting a provider or counting call.
	historyHTTPTransport struct {
		bodies   [][]byte
		response string
	}

	// historyWireRequest decodes the actual SDK request body. Provider-specific
	// content remains raw JSON so tests can inspect native block kinds directly.
	historyWireRequest struct {
		Messages   []historyWireMessage `json:"messages"`
		Input      json.RawMessage      `json:"input"`
		System     json.RawMessage      `json:"system"`
		Tools      json.RawMessage      `json:"tools"`
		ToolConfig json.RawMessage      `json:"toolConfig"` //nolint:tagliatelle // Bedrock's wire field uses camel case.
	}

	historyWireMessage struct {
		Role    string                       `json:"role"`
		Content []map[string]json.RawMessage `json:"content"`
	}
)

func TestCompressBedrockPreservesOriginalMediaGroups(t *testing.T) {
	for _, fixture := range []struct {
		name  string
		count int
		part  model.Part
		kind  string
	}{
		{name: "images", count: 11, kind: "image", part: historyPNGImage(t)},
		{name: "documents", count: 3, kind: "document", part: model.DocumentPart{
			Name: "same document", Format: model.DocumentFormatTXT, Text: "synthetic document body", Cite: true,
		}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			client, transport := historyEncodedClient(t, "bedrock", "")
			history := historyMediaGroups(fixture.part, fixture.count)
			compressed, err := agentruntime.Compress(client, agentruntime.HistoryCompressionConfig{
				CompressAtTurns: 2, KeepMaxTurns: 1,
			})(t.Context(), history, nil)
			require.NoError(t, err)
			assert.Equal(t, history[len(history)-1], compressed[len(compressed)-1])
			require.Len(t, transport.bodies, 1)
			var request historyWireRequest
			require.NoError(t, json.Unmarshal(transport.bodies[0], &request))
			require.Len(t, request.Messages, 3)
			assert.NotEmpty(t, request.System)
			assert.Empty(t, request.ToolConfig)
			assert.Empty(t, request.Tools)
			historyAssertQuotedTools(t, request.Messages[0])
			for group, originalMessage := range []int{1, 3} {
				message := request.Messages[group+1]
				assert.Equal(t, "user", message.Role)
				require.Len(t, message.Content, fixture.count*2)
				for part := range fixture.count {
					assert.Contains(t, historyWireText(t, message.Content[part*2]),
						fmt.Sprintf("History message %d, part %d", originalMessage, part+1))
					native := message.Content[part*2+1]
					require.Len(t, native, 1)
					require.Contains(t, native, fixture.kind)
					if fixture.kind == "image" {
						var image struct {
							Format string `json:"format"`
							Source struct {
								Bytes []byte `json:"bytes"`
							} `json:"source"`
						}
						require.NoError(t, json.Unmarshal(native["image"], &image))
						assert.Equal(t, "png", image.Format)
						assert.Equal(t, fixture.part.(model.ImagePart).Bytes, image.Source.Bytes)
					} else {
						var document struct {
							Name   string `json:"name"`
							Format string `json:"format"`
							Source struct {
								Text string `json:"text"`
							} `json:"source"`
							Citations struct {
								Enabled bool `json:"enabled"`
							} `json:"citations"`
						}
						require.NoError(t, json.Unmarshal(native["document"], &document))
						assert.Equal(t, "same document", document.Name)
						assert.Equal(t, "txt", document.Format)
						assert.Equal(t, "synthetic document body", document.Source.Text)
						assert.True(t, document.Citations.Enabled)
					}
				}
			}
			transcript := historyWireText(t, request.Messages[0].Content[0])
			if fixture.kind == "image" {
				assert.NotContains(t, transcript, base64.StdEncoding.EncodeToString(fixture.part.(model.ImagePart).Bytes))
			}
			assert.NotContains(t, transcript, "synthetic document body")
		})
	}
}

func TestCompressAnthropicPreservesImageReferencesAndRejectsDocuments(t *testing.T) {
	t.Run("outgoing user groups retain references", func(t *testing.T) {
		client, transport := historyEncodedClient(t, "anthropic", "")
		image := historyPNGImage(t)
		_, err := agentruntime.Compress(client, agentruntime.HistoryCompressionConfig{
			CompressAtTurns: 2, KeepMaxTurns: 1,
		})(t.Context(), historyMediaGroups(image, 11), nil)
		require.NoError(t, err)
		require.Len(t, transport.bodies, 1)
		var request historyWireRequest
		require.NoError(t, json.Unmarshal(transport.bodies[0], &request))
		require.Len(t, request.Messages, 3)
		assert.NotEmpty(t, request.System)
		assert.Empty(t, request.Tools)
		historyAssertQuotedTools(t, request.Messages[0])
		for group, originalMessage := range []int{1, 3} {
			message := request.Messages[group+1]
			assert.Equal(t, "user", message.Role)
			require.Len(t, message.Content, 22)
			for part := range 11 {
				assert.Contains(t, historyWireText(t, message.Content[part*2]),
					fmt.Sprintf("History message %d, part %d", originalMessage, part+1))
				native := message.Content[part*2+1]
				assert.JSONEq(t, `"image"`, string(native["type"]))
				var source struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      []byte `json:"data"`
				}
				require.NoError(t, json.Unmarshal(native["source"], &source))
				assert.Equal(t, "base64", source.Type)
				assert.Equal(t, "image/png", source.MediaType)
				assert.Equal(t, image.Bytes, source.Data)
			}
		}
	})
	t.Run("unsupported documents fail before transport", func(t *testing.T) {
		client, transport := historyEncodedClient(t, "anthropic", "")
		document := model.DocumentPart{Name: "spec", Format: model.DocumentFormatTXT, Text: "evidence", Cite: true}
		_, err := agentruntime.Compress(client, agentruntime.HistoryCompressionConfig{
			CompressAtTurns: 2, KeepMaxTurns: 1,
		})(t.Context(), historyMediaGroups(document, 3), nil)
		require.EqualError(t, err, "anthropic: unsupported user message part model.DocumentPart")
		assert.Empty(t, transport.bodies)
	})
}

func TestCompressCitedSummaryReencodesAsTextAcrossProviders(t *testing.T) {
	response := `{"output":{"message":{"role":"assistant","content":[{"citationsContent":{"content":[{"text":"The document supports this sentence."}],"citations":[{"title":"same document","source":"original-source","location":{"documentPage":{"documentIndex":0,"start":1,"end":1}},"sourceContent":[{"text":"supporting excerpt"}]}]}}]}},"stopReason":"end_turn","usage":{"inputTokens":4,"outputTokens":3,"totalTokens":7},"metrics":{"latencyMs":1}}`
	client, transport := historyEncodedClient(t, "bedrock", response)
	document := model.DocumentPart{Name: "same document", Format: model.DocumentFormatTXT, Text: "supporting excerpt", Cite: true}
	compressed, err := agentruntime.Compress(client, agentruntime.HistoryCompressionConfig{
		CompressAtTurns: 2, KeepMaxTurns: 1,
	})(t.Context(), historyMediaGroups(document, 3), nil)
	require.NoError(t, err)
	require.Len(t, transport.bodies, 1)
	require.Len(t, compressed, 3)
	summary := compressed[1]
	require.Len(t, summary.Parts, 1)
	text, ok := summary.Parts[0].(model.TextPart)
	require.True(t, ok)
	for _, evidence := range []string{"The document supports this sentence.", "same document", "original-source", "supporting excerpt", `"document_index":0`} {
		assert.Contains(t, text.Text, evidence)
	}
	for _, name := range []string{"bedrock", "anthropic", "openai"} {
		t.Run(name, func(t *testing.T) {
			nextClient, nextTransport := historyEncodedClient(t, name, "")
			_, err := nextClient.Complete(t.Context(), &model.Request{Messages: []*model.Message{
				summary, compressed[len(compressed)-1],
			}})
			require.NoError(t, err)
			require.Len(t, nextTransport.bodies, 1)
			var request historyWireRequest
			require.NoError(t, json.Unmarshal(nextTransport.bodies[0], &request))
			assert.Empty(t, request.Tools)
			assert.Empty(t, request.ToolConfig)
			if name == "openai" {
				var input []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				}
				require.NoError(t, json.Unmarshal(request.Input, &input))
				require.NotEmpty(t, input)
				var summaryText string
				require.NoError(t, json.Unmarshal(input[0].Content, &summaryText))
				assert.Equal(t, text.Text, summaryText)
			} else {
				var system []map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(request.System, &system))
				require.Len(t, system, 1)
				assert.Equal(t, text.Text, historyWireText(t, system[0]))
			}
			encoded := string(nextTransport.bodies[0])
			assert.Contains(t, encoded, "The document supports this sentence.")
			assert.NotContains(t, encoded, `"citationsContent":`)
			assert.NotContains(t, encoded, `"citations":`)
			assert.NotContains(t, encoded, `"annotations":`)
			assert.NotContains(t, encoded, `"toolUse":`)
			assert.NotContains(t, encoded, `"tool_result"`)
		})
	}
}

// historyEncodedClient installs a recording transport beneath each real SDK and
// wraps its provider with the same validated model client used by Compress.
func historyEncodedClient(t *testing.T, name, response string) (model.Client, *historyHTTPTransport) {
	t.Helper()
	transport := &historyHTTPTransport{response: response}
	httpClient := &http.Client{Transport: transport}
	var provider model.Provider
	var err error
	switch name {
	case "bedrock":
		if response == "" {
			transport.response = `{"output":{"message":{"role":"assistant","content":[{"text":"Summary complete."}]}},"stopReason":"end_turn","usage":{"inputTokens":4,"outputTokens":3,"totalTokens":7},"metrics":{"latencyMs":1}}`
		}
		sdkClient := bedrockruntime.NewFromConfig(aws.Config{
			Region: "us-east-1", Credentials: aws.CredentialsProviderFunc(historyTestCredentials), HTTPClient: httpClient,
		})
		provider, err = bedrock.NewProvider(sdkClient, bedrock.Options{
			DefaultModel: "amazon.nova-pro-v1:0", SmallModel: "amazon.nova-pro-v1:0",
		})
	case "anthropic":
		transport.response = `{"id":"msg_test","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","content":[{"type":"text","text":"Summary complete."}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":3}}`
		sdkClient := anthropicsdk.NewClient(anthropicoption.WithAPIKey("synthetic"),
			anthropicoption.WithBaseURL("https://anthropic.test"), anthropicoption.WithHTTPClient(httpClient),
			anthropicoption.WithMaxRetries(0))
		provider, err = anthropic.NewProvider(&sdkClient.Messages, anthropic.Options{
			DefaultModel: "claude-sonnet-4-20250514", SmallModel: "claude-sonnet-4-20250514", MaxTokens: 1024,
		})
	case "openai":
		transport.response = `{"id":"resp_test","object":"response","status":"completed","model":"gpt-4.1","output":[{"id":"msg_test","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Summary complete.","annotations":[]}]}],"usage":{"input_tokens":4,"output_tokens":3,"total_tokens":7}}`
		sdkClient := openaisdk.NewClient(openaioption.WithAPIKey("synthetic"),
			openaioption.WithBaseURL("https://openai.test"), openaioption.WithHTTPClient(httpClient),
			openaioption.WithMaxRetries(0))
		provider, err = openai.NewProvider(openai.Options{Client: &sdkClient.Responses, DefaultModel: "gpt-4.1"})
	default:
		t.Fatalf("unrecognized test provider %q", name)
	}
	require.NoError(t, err)
	client, err := model.NewClient(provider)
	require.NoError(t, err)
	return client, transport
}

// historyMediaGroups places media in two original user messages, separated by
// reasoning-only assistant content. The last user message remains exact.
func historyMediaGroups(part model.Part, count int) []*model.Message {
	first := make([]model.Part, 1, 1+count)
	first[0] = model.TextPart{Text: "First supplied evidence."}
	second := make([]model.Part, 1, 1+count)
	second[0] = model.TextPart{Text: "Second supplied evidence."}
	for range count {
		first = append(first, part)
		second = append(second, part)
	}
	return []*model.Message{
		{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "Original system instruction."}}},
		{Role: model.ConversationRoleUser, Parts: first},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ThinkingPart{Text: "reasoning-not-evidence", Signature: "original-signature"}}},
		{Role: model.ConversationRoleUser, Parts: second},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{ID: "lookup-1", Name: "read_temperature", Input: rawjson.Message(`{"equipment":"unit-A"}`)}}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.ToolResultPart{ToolUseID: "lookup-1", Content: "full synthetic tool error", IsError: true}}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Continue the current request."}}},
	}
}

func historyAssertQuotedTools(t *testing.T, message historyWireMessage) {
	t.Helper()
	assert.Equal(t, "user", message.Role)
	require.Len(t, message.Content, 1)
	text := historyWireText(t, message.Content[0])
	for _, evidence := range []string{"read_temperature", "unit-A", "full synthetic tool error"} {
		assert.Contains(t, text, evidence)
		assert.Equal(t, 1, strings.Count(text, evidence), "quoted value %q must not be repeated", evidence)
	}
	assert.Equal(t, 2, strings.Count(text, "lookup-1"), "the call and its result retain the same correlation ID")
	assert.NotContains(t, text, "reasoning-not-evidence")
	assert.NotContains(t, text, "original-signature")
}

func historyWireText(t *testing.T, part map[string]json.RawMessage) string {
	t.Helper()
	var text string
	require.NoError(t, json.Unmarshal(part["text"], &text))
	return text
}

func historyTestCredentials(context.Context) (aws.Credentials, error) {
	return aws.Credentials{AccessKeyID: "synthetic-key", SecretAccessKey: "synthetic-secret"}, nil
}

// historyPNGImage uses a real one-pixel PNG so native-image tests do not rely on
// bytes that merely claim to be an image in the declared format.
func historyPNGImage(t *testing.T) model.ImagePart {
	t.Helper()
	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))))
	return model.ImagePart{Format: model.ImageFormatPNG, Bytes: encoded.Bytes()}
}

func (transport *historyHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodPost || strings.Contains(request.URL.Path, "count") {
		return nil, fmt.Errorf("unexpected offline request: %s %s", request.Method, request.URL.Path)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, fmt.Errorf("read offline request: %w", err)
	}
	transport.bodies = append(transport.bodies, body)
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(transport.response)), Request: request,
	}, nil
}
