// This external-package test exercises the public provider and history APIs
// together so an image cannot be rejected solely because its JPEG carries more
// transfer bytes. Counting must leave the eventual inference request intact.
package openai_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/features/model/openai"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/runtime"
)

type imageRoundTripFunc func(*http.Request) (*http.Response, error)

func TestBedrockImageCountIgnoresJPEGTransferSize(t *testing.T) {
	for _, modelID := range []string{"global.openai.gpt-5.6-terra", "global.openai.gpt-6-sol"} {
		t.Run(modelID, func(t *testing.T) {
			var sent []byte
			client, err := openai.NewBedrock(t.Context(), "us-west-2", credentials.NewStaticCredentialsProvider("test", "test", "test"), openai.Options{DefaultModel: modelID}, option.WithHTTPClient(&http.Client{Transport: imageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				sent = body
				return imageJSONResponse(`{"id":"r","model":"` + modelID + `","status":"completed","output":[{"id":"m","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"A red rectangle.","annotations":[]}]}],"usage":{"input_tokens":2200,"output_tokens":4,"total_tokens":2204}}`), nil
			})}))
			require.NoError(t, err)
			var jpegBytes bytes.Buffer
			require.NoError(t, jpeg.Encode(&jpegBytes, image.NewRGBA(image.Rect(0, 0, 1568, 1176)), nil))
			imageBytes := jpegBytes.Bytes()
			// JPEG comment segments change transfer size without changing any pixels.
			padded := append([]byte(nil), imageBytes[:2]...)
			for range 20 {
				padded = append(padded, 0xff, 0xfe, 0xea, 0x62)
				padded = append(padded, bytes.Repeat([]byte("x"), 60000)...)
			}
			padded = append(padded, imageBytes[2:]...)
			request := &model.Request{}
			request.Messages = []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{
				model.TextPart{Text: "Describe this picture."},
				model.ImagePart{Format: model.ImageFormatJPEG, Bytes: imageBytes},
			}}}
			base, err := client.CountTokens(t.Context(), request)
			require.NoError(t, err)
			request.Messages[0].Parts[1] = model.ImagePart{Format: model.ImageFormatJPEG, Bytes: padded}
			before, err := json.Marshal(request)
			require.NoError(t, err)
			count, err := client.CountTokens(t.Context(), request)
			require.NoError(t, err)
			assert.Equal(t, base.InputTokens, count.InputTokens)
			assert.Less(t, count.InputTokens, 300000)
			assert.False(t, count.Exact)
			assert.Nil(t, sent, "counting must not send HTTP")
			after, err := json.Marshal(request)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			policy := runtime.Compress(client, runtime.HistoryCompressionConfig{
				AllowEstimatedTokens:     true,
				CompressAtMaxInputTokens: 300000,
				KeepMaxTurns:             1,
			})
			history, err := policy(t.Context(), request, client, nil)
			require.NoError(t, err, "a valid image must reach inference with the newest turn intact")
			assert.Equal(t, request.Messages, history.Messages)
			response, err := client.Complete(t.Context(), request)
			require.NoError(t, err)
			assert.Contains(t, string(sent), base64.StdEncoding.EncodeToString(padded))
			assert.Equal(t, 2200, response.Usage.InputTokens, "accounting uses provider usage, not the estimate")
			assert.NotEqual(t, count.InputTokens, response.Usage.InputTokens)
		})
	}
}

// RoundTrip records the prepared inference HTTP request without network access.
func (f imageRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// imageJSONResponse supplies one completed text response to the official SDK.
func imageJSONResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}
