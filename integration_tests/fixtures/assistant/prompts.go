// This file implements synthetic prompt producers for the independent MCP
// referee. The service returns typed messages; generated adapters encode their
// selected content and preserve the order supplied here.
package assistantapi

import (
	"context"
	"fmt"

	genassistant "example.com/assistant/gen/assistant"
)

// SimplePrompt returns fixed text instructions for the requested prompt.
func (s *assistantsrvc) SimplePrompt(_ context.Context) (*genassistant.RefereePromptResult, error) {
	message := &genassistant.RefereePromptMessage{Role: "user"}
	message.Content.SetText(&genassistant.RefereePromptText{Text: "This is a simple test prompt."})
	return &genassistant.RefereePromptResult{Messages: []*genassistant.RefereePromptMessage{message}}, nil
}

// ArgumentPrompt includes both supplied arguments in the returned instructions.
func (s *assistantsrvc) ArgumentPrompt(_ context.Context, p *genassistant.ArgumentPromptPayload) (*genassistant.RefereePromptResult, error) {
	message := &genassistant.RefereePromptMessage{Role: "user"}
	message.Content.SetText(&genassistant.RefereePromptText{
		Text: fmt.Sprintf("Use the first argument %s and the second argument %s.", p.Arg1, p.Arg2),
	})
	return &genassistant.RefereePromptResult{Messages: []*genassistant.RefereePromptMessage{message}}, nil
}

// ResourcePrompt preserves the supplied resource URI in an embedded text resource.
func (s *assistantsrvc) ResourcePrompt(_ context.Context, p *genassistant.ResourcePromptPayload) (*genassistant.RefereePromptResult, error) {
	resource := &genassistant.RefereePromptResource{}
	resource.Resource.SetText(&genassistant.RefereeEmbeddedText{
		URI:  p.ResourceURI,
		Text: "Synthetic resource contents for the requested prompt.",
	})
	message := &genassistant.RefereePromptMessage{Role: "user"}
	message.Content.SetResource(resource)
	return &genassistant.RefereePromptResult{Messages: []*genassistant.RefereePromptMessage{message}}, nil
}

// ImagePrompt returns the fixture's PNG followed by instructions about that image.
func (s *assistantsrvc) ImagePrompt(ctx context.Context) (*genassistant.RefereePromptResult, error) {
	data, err := s.BinaryResource(ctx)
	if err != nil {
		return nil, fmt.Errorf("load prompt image: %w", err)
	}
	image := &genassistant.RefereePromptMessage{Role: "user"}
	image.Content.SetImage(&genassistant.RefereePromptImage{Data: data, MimeType: "image/png"})
	text := &genassistant.RefereePromptMessage{Role: "user"}
	text.Content.SetText(&genassistant.RefereePromptText{Text: "Describe the supplied synthetic image."})
	return &genassistant.RefereePromptResult{Messages: []*genassistant.RefereePromptMessage{image, text}}, nil
}

// SuggestArgument derives two suggestions from the supplied partial argument.
func (s *assistantsrvc) SuggestArgument(_ context.Context, p *genassistant.SuggestArgumentPayload) (*genassistant.SuggestArgumentResult, error) {
	values := []string{p.Value + "-first", p.Value + "-second"}
	total, hasMore := int64(len(values)), false
	return &genassistant.SuggestArgumentResult{Values: values, Total: &total, HasMore: &hasMore}, nil
}
