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
	"math"
	"strings"

	"github.com/openai/openai-go/v3/packages/param"
	_ "golang.org/x/image/webp" // Register WebP header decoding for image token counting.

	"goa.design/goa-ai/runtime/agent/model"
)

// bedrockImageTokens counts images in user messages and correlated function
// outputs. It changes only the fresh counting request; inference keeps every
// original image byte and remains subject to provider validation.
func bedrockImageTokens(prepared *preparedRequest) (int, error) {
	var images []*param.Opt[string]
	for _, item := range prepared.request.Input.OfInputItemList {
		if item.OfMessage != nil {
			for _, content := range item.OfMessage.Content.OfInputItemContentList {
				if content.OfInputImage != nil {
					images = append(images, &content.OfInputImage.ImageURL)
				}
			}
		}
		if item.OfFunctionCallOutput != nil {
			for _, content := range item.OfFunctionCallOutput.Output.OfResponseFunctionCallOutputItemArray {
				if content.OfInputImage != nil {
					images = append(images, &content.OfInputImage.ImageURL)
				}
			}
		}
	}
	tokens := 0
	for _, address := range images {
		count, err := bedrockImageTokenCount(prepared.resolvedModelID, address.Value)
		if err != nil {
			return 0, err
		}
		if count > math.MaxInt-tokens {
			return 0, fmt.Errorf("openai: image token estimate exceeds supported integer range")
		}
		tokens += count
		*address = param.NewOpt("")
	}
	return tokens, nil
}

// bedrockImageTokenCount estimates image tokens from 32-pixel patches with a
// 1.2 multiplier. GPT-5.6 additionally applies its documented dimension and
// patch limits (https://developers.openai.com/api/docs/guides/images-vision).
// For GPT-6 Sol and GPT-6.1 Sol this is a local estimate, not a billing formula or an
// assertion about provider image limits. Response usage owns accounting.
func bedrockImageTokenCount(modelID, dataURL string) (int, error) {
	_, name, found := strings.Cut(modelID, ".openai.")
	if !found {
		name = strings.TrimPrefix(modelID, "openai.")
	}
	// Only GPT-5.6 applies documented image limits. The two supported Sol 6
	// models use the declared estimate and leave image acceptance to the provider.
	applyImageLimits := false
	switch name {
	case "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		applyImageLimits = true
	case "gpt-6-sol", "gpt-6.1-sol":
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
	if applyImageLimits {
		if largest := max(width, height); largest > 65535 {
			width, height = max(1, width*65535/largest), max(1, height*65535/largest)
		}
	}
	patches := ((width + 31) / 32) * ((height + 31) / 32)
	if applyImageLimits && patches > 30000 {
		return 0, fmt.Errorf("openai: image requires %d patches; model %q permits at most 30000 per image", patches, modelID)
	}
	tokens := (patches*6 + 4) / 5
	if tokens > math.MaxInt {
		return 0, fmt.Errorf("openai: image token estimate exceeds supported integer range")
	}
	return int(tokens), nil
}
