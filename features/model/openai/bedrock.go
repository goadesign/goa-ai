// Package openai binds the shared Responses adapter to the official OpenAI SDK's
// Bedrock Runtime transport. AWS owns authentication and routing; goa-ai owns
// exact tool schemas, transcript replay and validation of returned arguments.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdkbedrock "github.com/openai/openai-go/v3/bedrock"
	"github.com/openai/openai-go/v3/option"

	"goa.design/goa-ai/runtime/agent/model"
)

// bedrockProvider adds local request-size estimates only to the Bedrock route.
// Direct OpenAI providers retain their separate counting capability contract.
type bedrockProvider struct {
	*provider
}

var _ model.TokenCounter = (*bedrockProvider)(nil)

// NewBedrock constructs a validated OpenAI Responses client on Bedrock Runtime.
// region and credentials supply the AWS region and refreshable credentials.
// opts.Client must be absent because this constructor owns the official SDK
// client. The SDK verifies credentials during construction; errors retain their
// original cause for errors.Is and errors.As.
//
// Function tools advertise their complete generated schemas with strict:false;
// returned arguments are preserved and validated locally, never repaired.
// Cache-bearing requests and StructuredOutput are rejected before inference.
// CountTokens estimates the prepared request's byte size locally with Exact=false.
// This approximation is neither a native token count nor a guaranteed upper bound;
// callers that require exact counting must reject it.
// Model IDs and logical model classes are configured through opts as usual.
func NewBedrock(ctx context.Context, region string, credentials aws.CredentialsProvider, opts Options, requestOpts ...option.RequestOption) (model.Client, error) {
	provider, err := NewBedrockProvider(ctx, region, credentials, opts, requestOpts...)
	if err != nil {
		return nil, err
	}
	return model.NewClient(provider)
}

// NewBedrockProvider constructs the raw Bedrock Responses provider for middleware
// composition beneath model.NewClient. It has the same request contract as
// NewBedrock and implements model.TokenCounter with a local estimate. Request
// options configure the official SDK transport, including HTTP observation and
// retries. The SDK selects the regional Runtime endpoint by default and honors
// AWS_BEDROCK_BASE_URL. During request construction, its authenticated transport
// rejects option.WithBaseURL and authentication overrides before HTTP.
// SDK configuration and request errors preserve their original cause.
func NewBedrockProvider(ctx context.Context, region string, credentials aws.CredentialsProvider, opts Options, requestOpts ...option.RequestOption) (model.Provider, error) {
	if region == "" {
		return nil, errors.New("openai: Bedrock Responses requires an AWS region")
	}
	if credentials == nil {
		return nil, errors.New("openai: Bedrock Responses requires an AWS credentials provider")
	}
	if opts.Client != nil || opts.transport != nil {
		return nil, errors.New("openai: Bedrock Responses constructs its own SDK client")
	}
	client, err := sdkbedrock.NewClient(ctx, sdkbedrock.Config{
		Endpoint:               sdkbedrock.EndpointRuntime,
		AWSRegion:              region,
		AWSCredentialsProvider: credentials,
	}, requestOpts...)
	if err != nil {
		return nil, fmt.Errorf("openai: configure Bedrock Responses SDK: %w", err)
	}
	opts.Client = &client.Responses
	raw, err := newProvider(opts, true)
	if err != nil {
		return nil, err
	}
	return &bedrockProvider{provider: raw}, nil
}

// CountTokens validates and prepares the same SDK request used for inference,
// including encrypted reasoning and tool schemas, without sending HTTP. Its
// estimate is serialized bytes divided by three (rounded up), plus 500 tokens
// for framing. This is a wire-size approximation, not billing data or a hard
// context-window guarantee; opaque content need not tokenize like ordinary text.
func (c *bedrockProvider) CountTokens(ctx context.Context, req *model.Request) (model.TokenCount, error) {
	if err := ctx.Err(); err != nil {
		return model.TokenCount{}, err
	}
	if _, err := model.NewRequestContract(req); err != nil {
		return model.TokenCount{}, err
	}
	prepared, err := c.prepareRequest(req)
	if err != nil {
		return model.TokenCount{}, err
	}
	body, err := json.Marshal(prepared.request)
	if err != nil {
		return model.TokenCount{}, fmt.Errorf("openai: encode Bedrock token estimate request: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return model.TokenCount{}, err
	}
	return model.TokenCount{
		Model:       prepared.resolvedModelID,
		ModelClass:  prepared.resolvedModelClass,
		InputTokens: (len(body)+2)/3 + 500,
		Exact:       false,
	}, nil
}
