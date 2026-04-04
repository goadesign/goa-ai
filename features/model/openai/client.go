// Package openai provides a model.Client implementation backed by the OpenAI
// Responses API. The adapter keeps all provider quirks inside this package so
// planners and runtimes can continue speaking the provider-neutral model.Client
// contract.
package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/packages/ssestream"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type (
	// ResponsesClient captures the subset of the official OpenAI client used by
	// the adapter. It is satisfied by `*responses.ResponseService`.
	ResponsesClient interface {
		New(ctx context.Context, body responses.ResponseNewParams, opts ...option.RequestOption) (res *responses.Response, err error)
		NewStreaming(ctx context.Context, body responses.ResponseNewParams, opts ...option.RequestOption) (stream *ssestream.Stream[responses.ResponseStreamEventUnion])
	}

	// Options configures optional OpenAI adapter behavior.
	Options struct {
		// Client is the SDK-backed Responses API client used by the default
		// transport. It is required unless tests inject an internal transport.
		Client ResponsesClient

		// DefaultModel is the default model identifier used when Request.Model is
		// empty and no explicit model class override is selected.
		DefaultModel string

		// HighModel is the model identifier used when Request.ModelClass is
		// ModelClassHighReasoning and Request.Model is empty.
		HighModel string

		// SmallModel is the model identifier used when Request.ModelClass is
		// ModelClassSmall and Request.Model is empty.
		SmallModel string

		// MaxCompletionTokens is the default completion cap used when
		// Request.MaxTokens is zero.
		MaxCompletionTokens int

		// Temperature is the default sampling temperature used when
		// Request.Temperature is zero.
		Temperature float32

		// ThinkingEffort configures the OpenAI reasoning effort used when
		// Request.Thinking.Enable is true. Supported values are "low", "medium",
		// and "high".
		ThinkingEffort string

		// Ledger provides runtime-owned transcript rehydration for RunID-based
		// requests. When nil, RunID-based requests are not supported.
		Ledger transcript.LedgerSource

		transport transport
	}

	// Client implements model.Client on top of the OpenAI Responses API.
	Client struct {
		transport transport

		defaultModel string
		highModel    string
		smallModel   string

		maxCompletionTokens int
		temperature         float32
		thinkingEffort      string
		ledger              transcript.LedgerSource
	}

	// preparedRequest carries the provider-ready request plus the reversible
	// mappings needed to translate tool calls back to canonical goa-ai names.
	preparedRequest struct {
		request             responses.ResponseNewParams
		providerToCanonical map[string]string
		resolvedModelID     string
		resolvedModelClass  model.ModelClass
		structuredOutput    *model.StructuredOutput
	}

	// responseStream is the minimal streaming surface needed by the adapter.
	responseStream interface {
		Next() bool
		Current() responses.ResponseStreamEventUnion
		Err() error
		Close() error
	}

	// transport abstracts the provider-facing unary and streaming calls so the
	// adapter can test translation logic without constructing SDK clients.
	transport interface {
		Complete(ctx context.Context, request responses.ResponseNewParams) (*responses.Response, error)
		Stream(ctx context.Context, request responses.ResponseNewParams) responseStream
	}

	// sdkTransport is the production transport backed by the official OpenAI
	// Responses API client.
	sdkTransport struct {
		client ResponsesClient
	}
)

const (
	openAIProviderName   = "openai"
	thinkingEffortLow    = "low"
	thinkingEffortMedium = "medium"
	thinkingEffortHigh   = "high"
)

// New builds an OpenAI-backed model client from the provided options.
func New(opts Options) (*Client, error) {
	if opts.DefaultModel == "" {
		return nil, errors.New("openai: default model identifier is required")
	}
	if err := validateThinkingEffort(opts.ThinkingEffort); err != nil {
		return nil, err
	}
	tr := opts.transport
	if tr == nil {
		if opts.Client == nil {
			return nil, errors.New("openai: client is required")
		}
		tr = sdkTransport{client: opts.Client}
	}
	return &Client{
		transport:           tr,
		defaultModel:        opts.DefaultModel,
		highModel:           opts.HighModel,
		smallModel:          opts.SmallModel,
		maxCompletionTokens: opts.MaxCompletionTokens,
		temperature:         opts.Temperature,
		thinkingEffort:      opts.ThinkingEffort,
		ledger:              opts.Ledger,
	}, nil
}

// NewFromAPIKey constructs a client using the default OpenAI HTTP client.
func NewFromAPIKey(apiKey, defaultModel string) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("openai: api key is required")
	}
	client := openaisdk.NewClient(option.WithAPIKey(apiKey))
	service := client.Responses
	return New(Options{
		Client:       &service,
		DefaultModel: defaultModel,
	})
}

// Complete renders a unary response using the configured OpenAI client.
func (c *Client) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	prepared, err := c.prepareRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	resp, err := c.transport.Complete(ctx, prepared.request)
	if err != nil {
		return nil, wrapOpenAIError("responses.create", err)
	}
	return translateResponse(
		resp,
		prepared.providerToCanonical,
		prepared.resolvedModelID,
		prepared.resolvedModelClass,
		prepared.structuredOutput,
	)
}

// Stream renders a streaming response using the configured OpenAI client.
func (c *Client) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	prepared, err := c.prepareRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	stream := c.transport.Stream(ctx, prepared.request)
	if stream == nil {
		return nil, errors.New("openai: stream is nil")
	}
	return newOpenAIStreamer(
		ctx,
		stream,
		prepared.providerToCanonical,
		prepared.resolvedModelID,
		prepared.resolvedModelClass,
		prepared.structuredOutput,
	), nil
}

func (c *Client) prepareRequest(ctx context.Context, req *model.Request) (*preparedRequest, error) {
	if req == nil {
		return nil, errors.New("openai: request is required")
	}
	if err := validateRequestBoundary(req); err != nil {
		return nil, err
	}
	merged, err := transcript.RehydrateMessages(ctx, c.ledger, req.RunID, req.Messages)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	if len(merged) == 0 {
		return nil, errors.New("openai: messages are required")
	}
	modelID, modelClass, err := c.resolveModel(req)
	if err != nil {
		return nil, err
	}
	if modelID == "" {
		return nil, errors.New("openai: model identifier is required")
	}
	toolDefs, canonicalToProvider, providerToCanonical, err := encodeTools(req.Tools)
	if err != nil {
		return nil, err
	}
	input, err := encodeMessages(merged, canonicalToProvider)
	if err != nil {
		return nil, err
	}
	request := responses.ResponseNewParams{
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: input,
		},
		Model: modelID,
		Store: param.NewOpt(false),
	}
	if len(toolDefs) > 0 {
		request.Tools = toolDefs
	}
	if maxTokens := c.effectiveMaxCompletionTokens(req.MaxTokens); maxTokens > 0 {
		request.MaxOutputTokens = param.NewOpt(int64(maxTokens))
	}
	if temperature := c.effectiveTemperature(req.Temperature); temperature > 0 {
		request.Temperature = param.NewOpt(float64(temperature))
	}
	if req.Thinking != nil && req.Thinking.Enable {
		reasoning, include, err := c.effectiveThinkingRequest(req.Thinking)
		if err != nil {
			return nil, err
		}
		request.Reasoning = reasoning
		request.Include = append(request.Include, include...)
	}
	if req.StructuredOutput != nil {
		textConfig, ok, err := encodeStructuredOutput(req.StructuredOutput)
		if err != nil {
			return nil, err
		}
		if ok {
			request.Text = textConfig
		}
	}
	if req.ToolChoice != nil {
		choice, ok, err := encodeToolChoice(req.ToolChoice, canonicalToProvider)
		if err != nil {
			return nil, err
		}
		if ok {
			request.ToolChoice = choice
		}
	}
	return &preparedRequest{
		request:             request,
		providerToCanonical: providerToCanonical,
		resolvedModelID:     modelID,
		resolvedModelClass:  modelClass,
		structuredOutput:    req.StructuredOutput,
	}, nil
}

// validateRequestBoundary rejects request shapes that the OpenAI Responses path
// cannot represent without silent degradation.
func validateRequestBoundary(req *model.Request) error {
	if req == nil {
		return nil
	}
	if req.Cache != nil && (req.Cache.AfterSystem || req.Cache.AfterTools) {
		return errors.New("openai: request caching is not supported")
	}
	if req.StructuredOutput != nil && (len(req.Tools) > 0 || req.ToolChoice != nil) {
		return errors.New("openai: structured output cannot be combined with tools")
	}
	return nil
}

// resolveModel chooses the concrete provider model ID plus the logical model
// class associated with the request.
func (c *Client) resolveModel(req *model.Request) (string, model.ModelClass, error) {
	if req.Model != "" {
		return req.Model, req.ModelClass, nil
	}
	switch req.ModelClass {
	case model.ModelClassHighReasoning:
		if c.highModel != "" {
			return c.highModel, model.ModelClassHighReasoning, nil
		}
		return "", "", errors.New("openai: high-reasoning model class requested but HighModel is not configured")
	case model.ModelClassSmall:
		if c.smallModel != "" {
			return c.smallModel, model.ModelClassSmall, nil
		}
		return "", "", errors.New("openai: small model class requested but SmallModel is not configured")
	case "", model.ModelClassDefault:
		return c.defaultModel, model.ModelClassDefault, nil
	default:
		return "", "", fmt.Errorf("openai: unsupported model class %q", req.ModelClass)
	}
}

func (c *Client) effectiveMaxCompletionTokens(requested int) int {
	if requested > 0 {
		return requested
	}
	return c.maxCompletionTokens
}

func (c *Client) effectiveTemperature(requested float32) float32 {
	if requested > 0 {
		return requested
	}
	return c.temperature
}

// effectiveThinkingRequest maps the provider-neutral thinking request onto the
// OpenAI reasoning controls when the requested shape is representable.
func (c *Client) effectiveThinkingRequest(opts *model.ThinkingOptions) (shared.ReasoningParam, []responses.ResponseIncludable, error) {
	if opts == nil || !opts.Enable {
		return shared.ReasoningParam{}, nil, nil
	}
	if opts.BudgetTokens > 0 {
		return shared.ReasoningParam{}, nil, fmt.Errorf("openai: thinking budgets are not supported")
	}
	if opts.Interleaved {
		return shared.ReasoningParam{}, nil, fmt.Errorf("openai: interleaved thinking is not supported")
	}
	if c.thinkingEffort == "" {
		return shared.ReasoningParam{}, nil, fmt.Errorf("openai: thinking requires ThinkingEffort configuration")
	}
	return shared.ReasoningParam{
			Effort:  shared.ReasoningEffort(c.thinkingEffort),
			Summary: shared.ReasoningSummaryAuto,
		}, []responses.ResponseIncludable{
			responses.ResponseIncludableReasoningEncryptedContent,
		}, nil
}

func validateThinkingEffort(effort string) error {
	switch effort {
	case "", thinkingEffortLow, thinkingEffortMedium, thinkingEffortHigh:
		return nil
	default:
		return fmt.Errorf("openai: unsupported thinking effort %q", effort)
	}
}

func (t sdkTransport) Complete(ctx context.Context, request responses.ResponseNewParams) (*responses.Response, error) {
	return t.client.New(ctx, request)
}

func (t sdkTransport) Stream(ctx context.Context, request responses.ResponseNewParams) responseStream {
	return t.client.NewStreaming(ctx, request)
}

// isRateLimited reports whether err represents an OpenAI throttling response.
func isRateLimited(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, model.ErrRateLimited) {
		return true
	}
	var apiErr *openaisdk.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests || strings.EqualFold(fmt.Sprint(apiErr.Code), "rate_limit_exceeded")
	}
	return false
}

// wrapOpenAIError converts SDK errors into stable provider errors used by the
// runtime for retry and UX decisions.
func wrapOpenAIError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if isRateLimited(err) {
		pe := providerErrorFromSDK(operation, err)
		return errors.Join(model.ErrRateLimited, pe)
	}
	return providerErrorFromSDK(operation, err)
}

func providerErrorFromSDK(operation string, err error) error {
	if err == nil {
		return nil
	}
	var (
		status int
		code   string
		msg    string
	)
	var apiErr *openaisdk.Error
	if errors.As(err, &apiErr) {
		status = apiErr.StatusCode
		code = fmt.Sprint(apiErr.Code)
		msg = apiErr.Message
	}
	if msg == "" {
		msg = err.Error()
	}
	return newProviderError(operation, status, code, msg, err)
}

func providerErrorFromResponseFailure(operation string, code string, msg string, cause error) error {
	if msg == "" && cause != nil {
		msg = cause.Error()
	}
	status := inferredStatus(code)
	return newProviderError(operation, status, code, msg, cause)
}

func newProviderError(operation string, status int, code string, msg string, cause error) error {
	kind, retryable := classifyOpenAIError(status, code)
	return model.NewProviderError(
		openAIProviderName,
		operation,
		status,
		kind,
		code,
		msg,
		"",
		retryable,
		cause,
	)
}

func classifyOpenAIError(status int, code string) (model.ProviderErrorKind, bool) {
	normalized := strings.ToLower(code)
	switch {
	case status == http.StatusBadRequest:
		return model.ProviderErrorKindInvalidRequest, false
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return model.ProviderErrorKindAuth, false
	case status == http.StatusTooManyRequests:
		return model.ProviderErrorKindRateLimited, true
	case status >= http.StatusInternalServerError && status < 600:
		return model.ProviderErrorKindUnavailable, true
	case normalized == "rate_limit_exceeded":
		return model.ProviderErrorKindRateLimited, true
	case normalized == "server_error" || normalized == "vector_store_timeout":
		return model.ProviderErrorKindUnavailable, true
	case normalized == "invalid_prompt" || strings.HasPrefix(normalized, "invalid_"):
		return model.ProviderErrorKindInvalidRequest, false
	default:
		return model.ProviderErrorKindUnknown, false
	}
}

func inferredStatus(code string) int {
	switch strings.ToLower(code) {
	case "rate_limit_exceeded":
		return http.StatusTooManyRequests
	case "server_error", "vector_store_timeout":
		return http.StatusInternalServerError
	case "invalid_prompt":
		return http.StatusBadRequest
	default:
		if strings.HasPrefix(strings.ToLower(code), "invalid_") {
			return http.StatusBadRequest
		}
		return 0
	}
}
