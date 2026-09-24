// These tests distinguish image pixels from transfer bytes, exercise the
// per-image counting limits, and preserve the separate inference capability.
package openai

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestBedrockImageCountFormats(t *testing.T) {
	picture := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black, color.White})
	var pngBytes, jpegBytes, gifBytes bytes.Buffer
	require.NoError(t, png.Encode(&pngBytes, picture))
	require.NoError(t, jpeg.Encode(&jpegBytes, picture, nil))
	require.NoError(t, gif.Encode(&gifBytes, picture, nil))
	// This complete one-pixel lossless WebP decodes through x/image/webp.
	webpBytes, err := base64.StdEncoding.DecodeString("UklGRh4AAABXRUJQVlA4TBEAAAAvAAAAAAfQ//73v/+BiOh/AAA=")
	require.NoError(t, err)
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"png", pngBytes.Bytes()}, {"jpeg", jpegBytes.Bytes()},
		{"gif", gifBytes.Bytes()}, {"webp", webpBytes},
	} {
		t.Run(test.name, func(t *testing.T) {
			count, err := bedrockImageTokenCount(bedrockTestModel, "data:image/"+test.name+";base64,"+base64.StdEncoding.EncodeToString(test.data))
			require.NoError(t, err)
			assert.Equal(t, 2, count)
		})
	}
}

func TestBedrockImageCountDimensionsAndPerImageLimits(t *testing.T) {
	for _, test := range []struct {
		name                  string
		width, height, tokens int
		wantError             bool
	}{
		{name: "round partial patches", width: 33, height: 33, tokens: 5},
		{name: "ordinary photograph", width: 1568, height: 1176, tokens: 2176},
		{name: "below patch limit", width: 4799, height: 6368, tokens: 35820},
		{name: "at patch limit", width: 4800, height: 6400, tokens: 36000},
		{name: "above patch limit", width: 4801, height: 6400, wantError: true},
		{name: "at dimension limit", width: 65535, height: 32, tokens: 2458},
		{name: "above dimension limit", width: 65536, height: 32, tokens: 2458},
		{name: "thin image above dimension limit", width: 65536, height: 1, tokens: 2458},
	} {
		t.Run(test.name, func(t *testing.T) {
			picture := image.NewPaletted(image.Rect(0, 0, test.width, test.height), color.Palette{color.Black, color.White})
			var encoded bytes.Buffer
			require.NoError(t, png.Encode(&encoded, picture))
			count, err := bedrockImageTokenCount(bedrockTestModel, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(encoded.Bytes()))
			if test.wantError {
				require.ErrorContains(t, err, "at most 30000 per image")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.tokens, count)
			if test.name == "at patch limit" {
				raw, err := newProvider(Options{DefaultModel: bedrockTestModel, ThinkingEffort: "medium", transport: &mockTransport{}}, true)
				require.NoError(t, err)
				request := bedrockTestRequest()
				part := model.ImagePart{Format: model.ImageFormatPNG, Bytes: encoded.Bytes()}
				request.Messages = []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{part, part}}}
				prepared, err := raw.prepareRequest(request)
				require.NoError(t, err)
				total, err := bedrockImageTokens(prepared)
				require.NoError(t, err)
				assert.Equal(t, 72000, total, "the patch limit belongs to each image, not the request")
			}
		})
	}
}

// Sol estimates do not claim another model's hard image limits.
func TestBedrockSolImageEstimateDoesNotImposeGPT56Limits(t *testing.T) {
	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, image.NewGray(image.Rect(0, 0, 1, 1))))
	header := encoded.Bytes()
	binary.BigEndian.PutUint32(header[16:20], 65536)
	binary.BigEndian.PutUint32(header[20:24], 512)
	binary.BigEndian.PutUint32(header[29:33], crc32.ChecksumIEEE(header[12:29]))
	count, err := bedrockImageTokenCount("global.openai.gpt-6-sol", "data:image/png;base64,"+base64.StdEncoding.EncodeToString(header))
	require.NoError(t, err)
	assert.Equal(t, 39322, count)
}

func TestBedrockImageEstimateRejectsIntegerOverflow(t *testing.T) {
	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, image.NewGray(image.Rect(0, 0, 1, 1))))
	header := encoded.Bytes()
	binary.BigEndian.PutUint32(header[16:20], 1073741823)
	binary.BigEndian.PutUint32(header[20:24], 1073741823)
	binary.BigEndian.PutUint32(header[29:33], crc32.ChecksumIEEE(header[12:29]))
	raw, err := newProvider(Options{DefaultModel: "global.openai.gpt-6-sol", transport: &mockTransport{}}, true)
	require.NoError(t, err)
	parts := make([]model.Part, 7000)
	for i := range parts {
		parts[i] = model.ImagePart{Format: model.ImageFormatPNG, Bytes: header}
	}
	request := &model.Request{Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: parts}}}
	counter := &bedrockProvider{provider: raw}
	_, err = counter.CountTokens(t.Context(), request)
	require.ErrorContains(t, err, "token estimate exceeds supported integer range")
}

func TestBedrockImageCountExactDimensionFit(t *testing.T) {
	for _, test := range []struct {
		height uint32
		tokens int
	}{
		{height: 22001},
		{height: 4753, tokens: 9831},
	} {
		var encoded bytes.Buffer
		require.NoError(t, png.Encode(&encoded, image.NewGray(image.Rect(0, 0, 1, 1))))
		// DecodeConfig reads only the PNG header. Rewrite its dimensions and CRC
		// to exercise exact scaling without allocating a multi-gigapixel image.
		header := encoded.Bytes()
		binary.BigEndian.PutUint32(header[16:20], 3211215)
		binary.BigEndian.PutUint32(header[20:24], test.height)
		binary.BigEndian.PutUint32(header[29:33], crc32.ChecksumIEEE(header[12:29]))
		count, err := bedrockImageTokenCount(bedrockTestModel, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(header))
		if test.tokens == 0 {
			require.ErrorContains(t, err, "30720 patches")
			continue
		}
		require.NoError(t, err)
		assert.Equal(t, test.tokens, count)
	}
}

func TestBedrockImageCountCapabilityAndMalformedImage(t *testing.T) {
	for _, modelID := range []string{"global.openai.gpt-5.6-terra", "us.openai.gpt-5.6-sol", "us-gov.openai.gpt-5.6-luna", "openai.gpt-5.6-terra", "gpt-5.6-terra", "global.openai.gpt-6-sol", "us.openai.gpt-6-sol", "openai.gpt-6-sol", "gpt-6-sol"} {
		_, err := bedrockImageTokenCount(modelID, "data:image/jpeg;base64,invalid")
		require.ErrorContains(t, err, "decode image dimensions")
	}
	_, err := bedrockImageTokenCount("unrecognized-model", "data:image/jpeg;base64,invalid")
	require.ErrorIs(t, err, model.ErrTokenCountingUnsupported)
	_, err = bedrockImageTokenCount(bedrockTestModel, "invalid")
	require.ErrorContains(t, err, "requires an encoded image data URL")
	raw, err := newProvider(Options{DefaultModel: "unrecognized-model", ThinkingEffort: "medium", transport: &mockTransport{}}, true)
	require.NoError(t, err)
	request := bedrockTestRequest()
	counter := &bedrockProvider{provider: raw}
	_, err = counter.CountTokens(t.Context(), request)
	require.NoError(t, err, "text-only counting does not require an image rule")
	request.Messages[1].Parts = append(request.Messages[1].Parts, model.ImagePart{Format: model.ImageFormatJPEG, Bytes: []byte("provider-owned-image")})
	_, err = counter.CountTokens(t.Context(), request)
	require.ErrorIs(t, err, model.ErrTokenCountingUnsupported)
	prepared, err := raw.prepareRequest(request)
	require.NoError(t, err, "inference preparation does not decode images or require an image count rule")
	body, err := json.Marshal(prepared.request)
	require.NoError(t, err)
	assert.Contains(t, string(body), base64.StdEncoding.EncodeToString([]byte("provider-owned-image")))
}
