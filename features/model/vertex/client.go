// Gemini-on-Vertex raw provider core. Invariants:
//   - prepareRequest is the single translation entry point shared by
//     Complete, Stream, and CountTokens so gates apply uniformly.
//   - The adapter is stateless and never retries (see doc.go).

package vertex

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"math"
	"strings"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// GenerativeClient abstracts the genai Models service so tests can stub
	// it. *genai.Models satisfies this interface.
	GenerativeClient interface {
		GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
		GenerateContentStream(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error]
		CountTokens(ctx context.Context, model string, contents []*genai.Content, config *genai.CountTokensConfig) (*genai.CountTokensResponse, error)
	}

	// provider translates canonical requests to Gemini on Vertex.
	provider struct {
		models GenerativeClient
		opts   Options
	}

	// preparedRequest carries one request's translated inputs.
	preparedRequest struct {
		modelID          string
		modelClass       model.ModelClass
		contents         []*genai.Content
		config           *genai.GenerateContentConfig
		provToCanon      map[string]string
		structuredOutput *model.StructuredOutput
		toolCallIDs      *vertexToolCallIDAllocator
	}
)

// New builds a validated Gemini-on-Vertex client.
func New(models GenerativeClient, opts Options) (model.Client, error) {
	raw, err := NewProvider(models, opts)
	if err != nil {
		return nil, err
	}
	return model.NewClient(raw)
}

// NewProvider builds the raw Gemini-on-Vertex provider used beneath
// model.NewClient. Callers use it to install provider-side middleware before
// final validation.
func NewProvider(models GenerativeClient, opts Options) (model.Provider, error) {
	if models == nil {
		return nil, errors.New("vertex: generative client is required")
	}
	if opts.DefaultModel == "" {
		return nil, errors.New("vertex: default model is required")
	}
	if _, err := vertexInt32("default max tokens", opts.MaxTokens); err != nil {
		return nil, err
	}
	if _, err := vertexInt32("default thinking budget", opts.ThinkingBudget); err != nil {
		return nil, err
	}
	if err := validateVertexTemperature(opts.Temperature); err != nil {
		return nil, err
	}
	return &provider{models: models, opts: opts}, nil
}

// Complete performs one raw Gemini provider operation.
func (c *provider) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	contract, err := model.NewRequestContract(req)
	if err != nil {
		return nil, err
	}
	prep, err := c.prepareRequest(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.models.GenerateContent(ctx, prep.modelID, prep.contents, prep.config)
	if err != nil {
		return nil, wrapGeminiError("generate_content", err)
	}
	response, err := translateResponse(resp, prep.modelID, prep.modelClass, prep.provToCanon, prep.toolCallIDs)
	if err != nil {
		usage := translateUsage(nil, prep.modelID, prep.modelClass)
		if resp != nil {
			usage = translateUsage(resp.UsageMetadata, prep.modelID, prep.modelClass)
		}
		return nil, contract.RejectProviderOutput(&usage, err)
	}
	return response, nil
}

// CountTokens implements model.TokenCounter using the exact transcript and
// generation settings that Vertex inference would receive.
func (c *provider) CountTokens(ctx context.Context, req *model.Request) (model.TokenCount, error) {
	if _, err := model.NewRequestContract(req); err != nil {
		return model.TokenCount{}, err
	}
	prep, err := c.prepareRequest(req)
	if err != nil {
		return model.TokenCount{}, err
	}
	resp, err := c.models.CountTokens(ctx, prep.modelID, prep.contents, &genai.CountTokensConfig{
		SystemInstruction: prep.config.SystemInstruction,
		Tools:             prep.config.Tools,
		GenerationConfig: &genai.GenerationConfig{
			MaxOutputTokens:    prep.config.MaxOutputTokens,
			ResponseJsonSchema: prep.config.ResponseJsonSchema,
			ResponseMIMEType:   prep.config.ResponseMIMEType,
			Temperature:        prep.config.Temperature,
			ThinkingConfig:     prep.config.ThinkingConfig,
		},
	})
	if err != nil {
		return model.TokenCount{}, wrapGeminiError("count_tokens", err)
	}
	return model.TokenCount{
		Model:       prep.modelID,
		ModelClass:  prep.modelClass,
		InputTokens: int(resp.TotalTokens),
		Exact:       true,
	}, nil
}

// prepareRequest translates a model.Request into Gemini call inputs,
// applying capability gates shared by Complete, Stream, and CountTokens.
func (c *provider) prepareRequest(req *model.Request) (*preparedRequest, error) {
	if req == nil {
		return nil, errors.New("vertex: request is required")
	}
	modelID, err := c.opts.resolveModelID(req)
	if err != nil {
		return nil, err
	}
	if req.StructuredOutput != nil && len(req.Tools) > 0 {
		return nil, model.ErrStructuredOutputUnsupported
	}
	if req.Cache != nil {
		return nil, errors.New("vertex: cache options are not supported")
	}
	canonToProv, provToCanon, err := buildToolNameMaps(req.Tools)
	if err != nil {
		return nil, err
	}
	system, contents, err := encodeContents(req.Messages, canonToProv)
	if err != nil {
		return nil, err
	}
	if len(contents) == 0 {
		return nil, errors.New("vertex: request has no user or assistant messages")
	}
	config := &genai.GenerateContentConfig{SystemInstruction: system}
	if maxTokens := req.MaxTokens; maxTokens > 0 {
		config.MaxOutputTokens, err = vertexInt32("max tokens", maxTokens)
		if err != nil {
			return nil, err
		}
	} else if c.opts.MaxTokens > 0 {
		config.MaxOutputTokens, err = vertexInt32("default max tokens", c.opts.MaxTokens)
		if err != nil {
			return nil, err
		}
	}
	temperature := req.Temperature
	if temperature == 0 {
		temperature = c.opts.Temperature
	}
	if temperature != 0 {
		if err := validateVertexTemperature(temperature); err != nil {
			return nil, err
		}
		config.Temperature = genai.Ptr(temperature)
	}
	if len(req.Tools) > 0 {
		tools, err := encodeTools(req.Tools, canonToProv)
		if err != nil {
			return nil, err
		}
		config.Tools = tools
		config.ToolConfig, err = encodeToolConfig(req.ToolChoice, canonToProv)
		if err != nil {
			return nil, err
		}
	}
	if req.StructuredOutput != nil {
		schema, err := normalizeSchema(req.StructuredOutput.Schema)
		if err != nil {
			return nil, err
		}
		config.ResponseMIMEType = "application/json"
		config.ResponseJsonSchema = schema
	}
	switch {
	case req.Thinking != nil && req.Thinking.Enable:
		budget := req.Thinking.BudgetTokens
		if isGemini3Model(modelID) {
			if budget > 0 {
				return nil, errors.New("vertex: Gemini 3 uses thinking levels and does not accept a token budget")
			}
			config.ThinkingConfig = &genai.ThinkingConfig{IncludeThoughts: true}
			break
		}
		if budget == 0 {
			budget = c.opts.ThinkingBudget
		}
		tc := &genai.ThinkingConfig{IncludeThoughts: true}
		if budget > 0 {
			converted, err := vertexInt32("thinking budget", budget)
			if err != nil {
				return nil, err
			}
			tc.ThinkingBudget = genai.Ptr(converted)
		}
		config.ThinkingConfig = tc
	case req.Thinking != nil:
		if isGemini3Model(modelID) {
			return nil, errors.New("vertex: Gemini 3 does not support disabling thinking")
		}
		config.ThinkingConfig = &genai.ThinkingConfig{ThinkingBudget: genai.Ptr(int32(0))}
	}
	return &preparedRequest{
		modelID:          modelID,
		modelClass:       req.ModelClass,
		contents:         contents,
		config:           config,
		provToCanon:      provToCanon,
		structuredOutput: req.StructuredOutput,
		toolCallIDs:      newVertexToolCallIDAllocator(req.Messages),
	}, nil
}

// isGemini3Model reports whether modelID uses Gemini 3 thinking levels instead
// of numeric token budgets.
func isGemini3Model(modelID string) bool {
	name := modelID
	if separator := strings.LastIndexByte(name, '/'); separator >= 0 {
		name = name[separator+1:]
	}
	return strings.HasPrefix(name, "gemini-3-") || strings.HasPrefix(name, "gemini-3.")
}

// vertexInt32 rejects values the Gemini SDK cannot represent before narrowing.
func vertexInt32(field string, value int) (int32, error) {
	if value < 0 || int64(value) > math.MaxInt32 {
		return 0, fmt.Errorf("vertex: %s must be between 0 and %d", field, int64(math.MaxInt32))
	}
	return int32(value), nil
}

// validateVertexTemperature enforces Gemini's sampling range.
func validateVertexTemperature(temperature float32) error {
	value := float64(temperature)
	if math.IsNaN(value) || math.IsInf(value, 0) || temperature < 0 || temperature > 2 {
		return errors.New("vertex: temperature must be between 0 and 2")
	}
	return nil
}
