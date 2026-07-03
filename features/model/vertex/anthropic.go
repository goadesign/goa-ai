// This file: Claude-on-Vertex constructor. The Messages translator is
// reused verbatim from features/model/anthropic — zero new translation
// code — by handing it an Anthropic SDK client built with the SDK's vertex
// transport, which rewrites /v1/messages to the Vertex rawPredict endpoints
// and handles ADC auth. This file adds only what the anthropic package
// lacks for Vertex: error classification. anthropic.isRateLimited only
// detects errors that already wrap model.ErrRateLimited, so it never
// recognizes a real SDK 429 (*anthropic.Error with StatusCode 429).
// anthropicErrorMapper decorates the reused client — and wraps the streamer
// it returns so mid-stream Recv errors are classified too, not just
// call-establishment failures — so the adaptive rate-limit middleware
// observes genuine Vertex throttling on both paths.

package vertex

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	// Temperature is the default sampling temperature.
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
	return &anthropicErrorMapper{next: inner}, nil
}

// anthropicErrorMapper decorates a model.Client with the goa-ai provider
// error contract for Anthropic SDK failures. It exists solely to fix the
// upstream gap in features/model/anthropic: isRateLimited there only
// detects errors that already wrap model.ErrRateLimited, so real SDK 429s
// (surfaced as *anthropic.Error) are never classified as rate limits.
type anthropicErrorMapper struct {
	next model.Client
}

// Complete implements model.Client.
func (m *anthropicErrorMapper) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	resp, err := m.next.Complete(ctx, req)
	if err != nil {
		return nil, wrapAnthropicVertexError("complete", err)
	}
	return resp, nil
}

// Stream implements model.Client. The returned streamer is itself wrapped
// so mid-stream failures surfacing through Recv are classified too, not
// just stream-establishment errors.
func (m *anthropicErrorMapper) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	s, err := m.next.Stream(ctx, req)
	if err != nil {
		return nil, wrapAnthropicVertexError("stream", err)
	}
	return &anthropicStreamerMapper{next: s}, nil
}

// anthropicStreamerMapper decorates a model.Streamer so errors returned by
// Recv mid-stream go through the same classification as call-establishment
// errors. io.EOF (normal stream termination) and context cancellation or
// deadline errors pass through unmapped: they are consumer-side flow
// control, not provider failures.
type anthropicStreamerMapper struct {
	next model.Streamer
}

// Recv implements model.Streamer.
func (s *anthropicStreamerMapper) Recv() (model.Chunk, error) {
	chunk, err := s.next.Recv()
	if err == nil ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return chunk, err
	}
	return chunk, wrapAnthropicVertexError("stream_recv", err)
}

// Close implements model.Streamer.
func (s *anthropicStreamerMapper) Close() error { return s.next.Close() }

// Metadata implements model.Streamer.
func (s *anthropicStreamerMapper) Metadata() map[string]any { return s.next.Metadata() }

// wrapAnthropicVertexError classifies Anthropic SDK errors (which carry the
// Vertex HTTP status on *anthropic.Error) into the provider error contract,
// using the same status-to-kind table as wrapGeminiError (see errors.go).
func wrapAnthropicVertexError(operation string, err error) error {
	if err == nil {
		return nil
	}
	status := 0
	message := ""
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) {
		status = apiErr.StatusCode
		message = anthropicErrorMessage(apiErr)
	} else {
		message = err.Error()
	}
	return classifyProviderError(anthropicProviderName, operation, status, message, err)
}

// anthropicErrorMessage safely renders an *anthropic.Error's message.
//
// (*anthropic.Error).Error() unconditionally dereferences both Request and
// Response (see internal/apierror/apierror.go), which the SDK always
// populates when it constructs the error from a live HTTP round trip but
// which are nil on any error built without one — including hand-constructed
// test doubles. Calling apiErr.Error() in that case panics with a nil
// pointer dereference instead of returning a string, so this falls back to
// a status-only message whenever either field is missing.
func anthropicErrorMessage(apiErr *sdk.Error) string {
	if apiErr.Request == nil || apiErr.Response == nil {
		return fmt.Sprintf("anthropic api error: status %d", apiErr.StatusCode)
	}
	return apiErr.Error()
}
