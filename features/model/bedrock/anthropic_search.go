// Package bedrock uses an unversioned Claude search tool declaration on InvokeModel
// and an explicit beta flag. This private SDK wrapper owns those endpoint
// differences; applications keep the existing Anthropic constructor.
package bedrock

import (
	"context"
	"fmt"
	"slices"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"

	"goa.design/goa-ai/features/model/internal/claudecaps"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	anthropicMessages struct {
		*sdk.MessageService
	}
)

const bedrockSearchBeta = "tool-search-tool-2025-10-19"

// New translates the hosted-search declaration before SDK signing and sending.
func (c *anthropicMessages) New(ctx context.Context, body sdk.MessageNewParams, opts ...option.RequestOption) (*sdk.Message, error) {
	body, opts, err := bedrockSearchParams(body, opts)
	if err != nil {
		return nil, err
	}
	return c.MessageService.New(ctx, body, opts...)
}

// NewStreaming applies the same contract to InvokeModelWithResponseStream.
func (c *anthropicMessages) NewStreaming(ctx context.Context, body sdk.MessageNewParams, opts ...option.RequestOption) *ssestream.Stream[sdk.MessageStreamEventUnion] {
	body, opts, err := bedrockSearchParams(body, opts)
	if err != nil {
		return ssestream.NewStream[sdk.MessageStreamEventUnion](nil, err)
	}
	return c.MessageService.NewStreaming(ctx, body, opts...)
}

func bedrockSearchParams(body sdk.MessageNewParams, opts []option.RequestOption) (sdk.MessageNewParams, []option.RequestOption, error) {
	for _, message := range body.Messages {
		if message.Role == sdk.MessageParamRoleSystem && !claudecaps.BedrockToolChangesSupported(body.Model) {
			return body, nil, fmt.Errorf("bedrock: model %q does not support native tool availability changes: %w",
				body.Model, model.ErrToolSearchUnsupported)
		}
	}
	for i, tool := range body.Tools {
		if tool.OfToolSearchToolRegex20251119 == nil {
			continue
		}
		body.Tools = slices.Clone(body.Tools)
		search := *tool.OfToolSearchToolRegex20251119
		search.Type = sdk.ToolSearchToolRegex20251119TypeToolSearchToolRegex
		body.Tools[i].OfToolSearchToolRegex20251119 = &search
		opts = append(slices.Clone(opts), option.WithHeaderAdd("anthropic-beta", bedrockSearchBeta))
		break
	}
	return body, opts, nil
}
