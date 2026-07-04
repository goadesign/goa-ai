// This file: Claude-on-Vertex constructor. The Messages translator AND
// error classification live in features/model/anthropic — zero new
// translation or error-mapping code here — by handing that package's client
// an Anthropic SDK client built with the SDK's vertex transport, which
// rewrites /v1/messages to the Vertex rawPredict endpoints and handles ADC
// auth. This file is pure construction: it validates Vertex-specific
// options and wires the vertex transport into anthropicprovider.New.

package vertex

import (
	"context"
	"errors"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	sdkvertex "github.com/anthropics/anthropic-sdk-go/vertex"

	anthropicprovider "goa.design/goa-ai/features/model/anthropic"
	"goa.design/goa-ai/runtime/agent/model"
)

// AnthropicOptions configures the Claude-on-Vertex model client. Model IDs
// are Vertex publisher IDs: bare first-party names for current-generation
// models (e.g. "claude-sonnet-5") or dated snapshots with an @ separator
// (e.g. "claude-haiku-4-5@20251001"). Never use Bedrock's "anthropic."
// prefix here.
type AnthropicOptions struct {
	// ProjectID is the GCP project hosting the Vertex endpoint.
	ProjectID string
	// Region is the Vertex region ("global", "us", "eu", or a specific
	// region such as "us-east5").
	Region string
	// DefaultModel serves requests with no explicit model or class override.
	DefaultModel string
	// HighModel serves ModelClassHighReasoning requests when set.
	HighModel string
	// SmallModel serves ModelClassSmall requests when set.
	SmallModel string
	// MaxTokens caps output tokens when the request does not set MaxTokens.
	MaxTokens int
	// Temperature is the default sampling temperature. It has no effect for
	// models that no longer accept the parameter (Claude Opus 4.7+, Claude
	// Sonnet 5+, and the Fable/Mythos generation) — anthropicprovider omits
	// it from the wire request for those models rather than forwarding a
	// value guaranteed to 400. See
	// features/model/internal/claudecaps.TemperatureSupported for the exact
	// rule.
	Temperature float64
	// ThinkingBudget is the default thinking token budget.
	ThinkingBudget int64
}

// NewAnthropicClient builds a Claude-on-Vertex model client using
// Application Default Credentials.
//
//nolint:unparam // model.Client is consumed by wiring code outside this package; these unit tests only exercise the validation-error paths.
func NewAnthropicClient(ctx context.Context, opts AnthropicOptions) (model.Client, error) {
	if strings.TrimSpace(opts.ProjectID) == "" {
		return nil, errors.New("vertex: project id is required")
	}
	if strings.TrimSpace(opts.Region) == "" {
		return nil, errors.New("vertex: region is required")
	}
	if strings.TrimSpace(opts.DefaultModel) == "" {
		return nil, errors.New("vertex: default model is required")
	}
	client := sdk.NewClient(sdkvertex.WithGoogleAuth(ctx, opts.Region, opts.ProjectID))
	inner, err := anthropicprovider.New(&client.Messages, anthropicprovider.Options{
		DefaultModel:   opts.DefaultModel,
		HighModel:      opts.HighModel,
		SmallModel:     opts.SmallModel,
		MaxTokens:      opts.MaxTokens,
		Temperature:    opts.Temperature,
		ThinkingBudget: opts.ThinkingBudget,
	})
	if err != nil {
		return nil, err
	}
	return inner, nil
}
