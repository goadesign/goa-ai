// Package openai counts Bedrock Responses images by their dimensions, not their
// base64 transport size. Only CountTokens uses this preparation: completion and
// streaming retain the original image bytes and provider validation behavior.
package openai

import (
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"  // Register GIF header decoding for image token counting.
	_ "image/jpeg" // Register JPEG header decoding for image token counting.
	_ "image/png"  // Register PNG header decoding for image token counting.
	"strings"

	"github.com/openai/openai-go/v3/packages/param"
	_ "golang.org/x/image/webp" // Register WebP header decoding for image token counting.

	"goa.design/goa-ai/runtime/agent/model"
)

// bedrockImageTokens replaces image URLs only in the freshly prepared counting
// request and returns their separate token cost. The message encoder puts every
// canonical user image in an OfMessage content list with a base64 data URL.
func bedrockImageTokens(prepared *preparedRequest) (int, error) {
	tokens := 0
	for _, item := range prepared.request.Input.OfInputItemList {
		if item.OfMessage == nil {
			continue
		}
		for _, content := range item.OfMessage.Content.OfInputItemContentList {
			img := content.OfInputImage
			if img == nil {
				continue
			}
			count, err := bedrockImageTokenCount(prepared.resolvedModelID, img.ImageURL.Value)
			if err != nil {
				return 0, err
			}
			tokens += count
			img.ImageURL = param.NewOpt("")
		}
	}
	return tokens, nil
}

// bedrockImageTokenCount applies GPT-5.6's documented auto/original image rule:
// fit each image within 65,535 pixels per side, count 32-pixel patches, reject
// more than 30,000 patches for that image, then round up the 1.2 multiplier.
// See https://developers.openai.com/api/docs/guides/images-vision.
// Unknown model rules fail counting rather than guessing from encoded bytes.
func bedrockImageTokenCount(modelID, dataURL string) (int, error) {
	_, name, found := strings.Cut(modelID, ".openai.")
	if !found {
		name = strings.TrimPrefix(modelID, "openai.")
	}
	switch name {
	case "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
	default:
		return 0, fmt.Errorf("openai: image token counting for model %q: %w", modelID, model.ErrTokenCountingUnsupported)
	}
	_, encoded, found := strings.Cut(dataURL, ",")
	if !found {
		return 0, fmt.Errorf("openai: image token counting requires an encoded image data URL")
	}
	config, _, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded)))
	if err != nil {
		return 0, fmt.Errorf("openai: decode image dimensions for token counting: %w", err)
	}
	width, height := int64(config.Width), int64(config.Height)
	if largest := max(width, height); largest > 65535 {
		width, height = max(1, width*65535/largest), max(1, height*65535/largest)
	}
	patches := ((width + 31) / 32) * ((height + 31) / 32)
	if patches > 30000 {
		return 0, fmt.Errorf("openai: image requires %d patches; model %q permits at most 30000 per image", patches, modelID)
	}
	return int((patches*6 + 4) / 5), nil
}
