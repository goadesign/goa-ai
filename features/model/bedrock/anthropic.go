// Package bedrock constructs Claude-on-Bedrock providers without translating Claude
// requests through the Bedrock Converse tool format. The Anthropic adapter owns
// Messages encoding and decoding, while the Anthropic SDK's Bedrock transport
// owns AWS request signing and InvokeModel streaming. A separate exact counter
// receives the same canonical request after its Bedrock inference-profile model
// ID is converted to the foundation model ID accepted by Bedrock Mantle.
package bedrock

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	sdkbedrock "github.com/anthropics/anthropic-sdk-go/bedrock"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/aws/aws-sdk-go-v2/aws"

	anthropicprovider "goa.design/goa-ai/features/model/anthropic"
	"goa.design/goa-ai/features/model/internal/modelid"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// AnthropicOptions configures Claude model selection, output limits, and
	// thinking defaults for Anthropic Messages over Amazon Bedrock.
	AnthropicOptions = anthropicprovider.Options

	// anthropicBedrockProvider sends inference through Anthropic Messages and
	// sends the equivalent request to the configured exact token counter.
	anthropicBedrockProvider struct {
		inference    model.Provider
		counter      model.TokenCounter
		defaultModel string
		highModel    string
		smallModel   string
	}
)

var (
	_ model.Provider     = (*anthropicBedrockProvider)(nil)
	_ model.TokenCounter = (*anthropicBedrockProvider)(nil)
)

// NewAnthropic constructs a validated Claude client that sends Anthropic
// Messages requests through Amazon Bedrock. The counter must count Anthropic
// Messages requests on an endpoint such as Bedrock Mantle; callers should not
// pass a counter for the Bedrock Converse representation. A counter built with
// the Anthropic adapter for Bedrock must set ToolExamplesInSchema so counting
// receives the same schema example representation as inference.
//
// Request options are installed on the Anthropic SDK client after the Bedrock
// transport. Applications use them for HTTP observation or other transport
// behavior that does not change the provider request contract.
func NewAnthropic(
	cfg aws.Config,
	counter model.TokenCounter,
	opts AnthropicOptions,
	requestOpts ...option.RequestOption,
) (model.Client, error) {
	raw, err := NewAnthropicProvider(cfg, counter, opts, requestOpts...)
	if err != nil {
		return nil, err
	}
	return model.NewClient(raw)
}

// NewAnthropicProvider constructs the raw Claude-on-Bedrock provider used
// beneath model.NewClient. It binds the Anthropic adapter to AWS, keeps authored
// tool examples inside input_schema because Bedrock rejects input_examples, and
// delegates exact counting after converting inference-profile model IDs to
// foundation IDs.
func NewAnthropicProvider(
	cfg aws.Config,
	counter model.TokenCounter,
	opts AnthropicOptions,
	requestOpts ...option.RequestOption,
) (model.Provider, error) {
	if cfg.Region == "" {
		return nil, errors.New("bedrock: AWS region is required for Anthropic Messages")
	}
	if counter == nil {
		return nil, errors.New("bedrock: Anthropic Messages requires an exact token counter")
	}

	clientOpts := make([]option.RequestOption, 0, len(requestOpts)+1)
	clientOpts = append(clientOpts, sdkbedrock.WithConfig(cfg))
	clientOpts = append(clientOpts, requestOpts...)
	client := sdk.NewClient(clientOpts...)

	opts.ToolExamplesInSchema = true
	inference, err := anthropicprovider.NewProvider(&client.Messages, opts)
	if err != nil {
		return nil, fmt.Errorf("bedrock: create Anthropic Messages provider: %w", err)
	}
	return &anthropicBedrockProvider{
		inference:    inference,
		counter:      counter,
		defaultModel: opts.DefaultModel,
		highModel:    opts.HighModel,
		smallModel:   opts.SmallModel,
	}, nil
}

// Complete sends one canonical request through Anthropic Messages on Bedrock.
func (c *anthropicBedrockProvider) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	effective, outputToolName, err := c.prepareRequest(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.inference.Complete(ctx, effective)
	if err != nil {
		return nil, err
	}
	if outputToolName == "" {
		return resp, nil
	}
	contract, err := model.NewRequestContract(req)
	if err != nil {
		return nil, err
	}
	if err := reifyStructuredOutputTool(resp, outputToolName); err != nil {
		return nil, contract.RejectResponse(model.OutputValidationStructuredOutput, resp, err)
	}
	return resp, nil
}

// Stream sends one canonical streaming request through Anthropic Messages on
// Bedrock and returns the Anthropic adapter's validated provider stream.
func (c *anthropicBedrockProvider) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	effective, outputToolName, err := c.prepareRequest(req)
	if err != nil {
		return nil, err
	}
	stream, err := c.inference.Stream(ctx, effective)
	if err != nil {
		return nil, err
	}
	if outputToolName == "" {
		return stream, nil
	}
	contract, err := model.NewRequestContract(req)
	if err != nil {
		return nil, errors.Join(err, stream.Close())
	}
	return newAnthropicStructuredOutputStreamer(stream, outputToolName, contract), nil
}

// CountTokens sends the same canonical request to the configured Anthropic
// counter after replacing a Bedrock cross-region inference profile with the
// foundation model ID accepted by the counting endpoint.
func (c *anthropicBedrockProvider) CountTokens(ctx context.Context, req *model.Request) (model.TokenCount, error) {
	if _, err := model.NewRequestContract(req); err != nil {
		return model.TokenCount{}, err
	}
	effective, _, err := c.prepareRequest(req)
	if err != nil {
		return model.TokenCount{}, err
	}
	resolved, err := modelid.Resolve(
		bedrockProviderName,
		effective,
		c.defaultModel,
		c.highModel,
		c.smallModel,
	)
	if err != nil {
		return model.TokenCount{}, err
	}
	foundation, err := FoundationModelID(resolved)
	if err != nil {
		return model.TokenCount{}, fmt.Errorf("bedrock: resolve Anthropic counting model: %w", err)
	}
	countReq := *effective
	countReq.Model = foundation
	return c.counter.CountTokens(ctx, &countReq)
}
