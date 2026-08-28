// Package openai provides raw and validated OpenAI Responses API adapters. Raw
// providers are available for provider-side middleware; ordinary constructors
// return the opaque model.Client validation boundary.
package openai

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/packages/ssestream"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"

	"goa.design/goa-ai/features/model/internal/outputvalidation"
	"goa.design/goa-ai/runtime/agent/model"
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

		transport transport
	}

	// provider translates canonical model requests to the OpenAI Responses API.
	provider struct {
		transport transport

		defaultModel string
		highModel    string
		smallModel   string

		maxCompletionTokens int
		temperature         float32
		thinkingEffort      string
	}

	// preparedRequest carries the provider-ready request plus the reversible
	// tool projection state needed to translate tool calls back to canonical
	// goa-ai names and payloads.
	preparedRequest struct {
		request            responses.ResponseNewParams
		codec              *toolCodec
		resolvedModelID    string
		resolvedModelClass model.ModelClass
		structuredOutput   *model.StructuredOutput
		outputProjection   *strictSchemaProjection
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

// New builds a validated OpenAI-backed model client.
func New(opts Options) (model.Client, error) {
	raw, err := NewProvider(opts)
	if err != nil {
		return nil, err
	}
	return model.NewClient(raw)
}

// NewProvider builds the raw OpenAI provider used beneath model.NewClient.
// Callers should use this constructor only to install provider-side middleware
// before final canonical output validation.
func NewProvider(opts Options) (model.Provider, error) {
	if opts.DefaultModel == "" {
		return nil, errors.New("openai: default model identifier is required")
	}
	if opts.MaxCompletionTokens < 0 {
		return nil, errors.New("openai: default max completion tokens cannot be negative")
	}
	if err := validateOpenAITemperature(opts.Temperature); err != nil {
		return nil, fmt.Errorf("openai: default %w", err)
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
	return &provider{
		transport:           tr,
		defaultModel:        opts.DefaultModel,
		highModel:           opts.HighModel,
		smallModel:          opts.SmallModel,
		maxCompletionTokens: opts.MaxCompletionTokens,
		temperature:         opts.Temperature,
		thinkingEffort:      opts.ThinkingEffort,
	}, nil
}

// NewFromAPIKey constructs a validated client using the default OpenAI HTTP
// client.
func NewFromAPIKey(apiKey, defaultModel string) (model.Client, error) {
	raw, err := NewProviderFromAPIKey(apiKey, defaultModel)
	if err != nil {
		return nil, err
	}
	return model.NewClient(raw)
}

// NewProviderFromAPIKey constructs a raw provider using the default OpenAI HTTP
// client.
func NewProviderFromAPIKey(apiKey, defaultModel string) (model.Provider, error) {
	if apiKey == "" {
		return nil, errors.New("openai: api key is required")
	}
	client := openaisdk.NewClient(option.WithAPIKey(apiKey))
	service := client.Responses
	return NewProvider(Options{
		Client:       &service,
		DefaultModel: defaultModel,
	})
}

// Complete renders a unary response using the configured OpenAI client.
func (c *provider) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	contract, err := model.NewRequestContract(req)
	if err != nil {
		return nil, err
	}
	prepared, err := c.prepareRequest(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.transport.Complete(ctx, prepared.request)
	if err != nil {
		return nil, wrapOpenAIError("responses.create", err)
	}
	response, err := translateResponse(
		resp,
		prepared.codec,
		prepared.resolvedModelID,
		prepared.resolvedModelClass,
		prepared.structuredOutput,
		prepared.outputProjection,
	)
	if err != nil {
		if _, providerFailure := model.AsProviderError(err); providerFailure {
			return nil, err
		}
		usage := translateUsage(resp.Usage, chooseModelID(resp.Model, prepared.resolvedModelID), prepared.resolvedModelClass)
		return nil, contract.RejectProviderOutput(
			outputvalidation.RequiredKind(err),
			&usage,
			err,
		)
	}
	return response, nil
}

// Stream renders a raw streaming response using the configured OpenAI client.
func (c *provider) Stream(ctx context.Context, req *model.Request) (model.Streamer, error) {
	contract, err := model.NewRequestContract(req)
	if err != nil {
		return nil, err
	}
	prepared, err := c.prepareRequest(req)
	if err != nil {
		return nil, err
	}
	stream := c.transport.Stream(ctx, prepared.request)
	if stream == nil {
		return nil, errors.New("openai: stream is nil")
	}
	streamer := newOpenAIStreamer(
		ctx,
		stream,
		prepared.codec,
		prepared.resolvedModelID,
		prepared.resolvedModelClass,
		prepared.structuredOutput,
		prepared.outputProjection,
		contract,
	)
	return streamer, nil
}

func (c *provider) prepareRequest(req *model.Request) (*preparedRequest, error) {
	if req == nil {
		return nil, errors.New("openai: request is required")
	}
	if err := validateRequestBoundary(req); err != nil {
		return nil, err
	}
	if len(req.Messages) == 0 {
		return nil, errors.New("openai: messages are required")
	}
	modelID, modelClass, err := c.resolveModel(req)
	if err != nil {
		return nil, err
	}
	if modelID == "" {
		return nil, errors.New("openai: model identifier is required")
	}
	toolDefs, codec, err := encodeTools(req.Tools, modelID)
	if err != nil {
		return nil, err
	}
	input, err := encodeMessages(req.Messages, codec.providerNames())
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
	if req.Thinking != nil && req.Thinking.Enable {
		if req.Temperature > 0 {
			return nil, errors.New("openai: temperature is not supported when thinking is enabled")
		}
		reasoning, include, err := c.effectiveThinkingRequest(req.Thinking)
		if err != nil {
			return nil, err
		}
		request.Reasoning = reasoning
		request.Include = append(request.Include, include...)
	} else if temperature := c.effectiveTemperature(req.Temperature); temperature > 0 {
		request.Temperature = param.NewOpt(float64(temperature))
	}
	var outputProjection *strictSchemaProjection
	if req.StructuredOutput != nil {
		textConfig, projection, ok, err := encodeStructuredOutput(req.StructuredOutput, modelID)
		if err != nil {
			return nil, err
		}
		if ok {
			request.Text = textConfig
			outputProjection = projection
		}
	}
	if req.ToolChoice != nil {
		choice, ok, err := encodeToolChoice(req.ToolChoice, codec.providerNames())
		if err != nil {
			return nil, err
		}
		if ok {
			request.ToolChoice = choice
		}
	}
	return &preparedRequest{
		request:            request,
		codec:              codec,
		resolvedModelID:    modelID,
		resolvedModelClass: modelClass,
		structuredOutput:   req.StructuredOutput,
		outputProjection:   outputProjection,
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
	if err := validateOpenAITemperature(req.Temperature); err != nil {
		return fmt.Errorf("openai: %w", err)
	}
	return nil
}

// validateOpenAITemperature enforces the Responses API sampling range before
// request construction.
func validateOpenAITemperature(temperature float32) error {
	value := float64(temperature)
	if math.IsNaN(value) || math.IsInf(value, 0) || temperature < 0 || temperature > 2 {
		return errors.New("temperature must be between 0 and 2")
	}
	return nil
}

// resolveModel chooses the concrete provider model ID plus the logical model
// class associated with the request.
func (c *provider) resolveModel(req *model.Request) (string, model.ModelClass, error) {
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

func (c *provider) effectiveMaxCompletionTokens(requested int) int {
	if requested > 0 {
		return requested
	}
	return c.maxCompletionTokens
}

func (c *provider) effectiveTemperature(requested float32) float32 {
	if requested > 0 {
		return requested
	}
	return c.temperature
}

// effectiveThinkingRequest maps the provider-neutral thinking request onto the
// OpenAI reasoning controls when the requested shape is representable.
func (c *provider) effectiveThinkingRequest(opts *model.ThinkingOptions) (shared.ReasoningParam, []responses.ResponseIncludable, error) {
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
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	providerErr := providerErrorFromSDK(operation, err)
	if errors.Is(providerErr, model.ErrRateLimited) {
		return providerErr
	}
	if isRateLimited(err) {
		return errors.Join(model.ErrRateLimited, providerErr)
	}
	return providerErr
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
	providerErr := model.NewProviderError(
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
	if kind == model.ProviderErrorKindRateLimited {
		return errors.Join(model.ErrRateLimited, providerErr)
	}
	return providerErr
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
