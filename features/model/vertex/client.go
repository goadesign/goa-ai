// Gemini-on-Vertex model.Client core. Invariants:
//   - prepareRequest is the single translation entry point shared by
//     Complete, Stream, and CountTokens so gates apply uniformly.
//   - The adapter is stateless and never retries (see doc.go).

package vertex

import (
	"context"
	"errors"
	"iter"

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

	// Client is the Gemini-on-Vertex implementation of model.Client.
	Client struct {
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
	}
)

// New builds a Gemini-on-Vertex client from a generative service and options.
func New(models GenerativeClient, opts Options) (*Client, error) {
	if models == nil {
		return nil, errors.New("vertex: generative client is required")
	}
	if opts.DefaultModel == "" {
		return nil, errors.New("vertex: default model is required")
	}
	return &Client{models: models, opts: opts}, nil
}

// Complete implements model.Client.
func (c *Client) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	prep, err := c.prepareRequest(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.models.GenerateContent(ctx, prep.modelID, prep.contents, prep.config)
	if err != nil {
		return nil, wrapGeminiError("generate_content", err)
	}
	return translateResponse(resp, prep.modelID, prep.modelClass, prep.provToCanon)
}

// CountTokens implements model.TokenCounter using Vertex's native counter.
// Replayed thinking parts are excluded per the TokenCounter contract.
func (c *Client) CountTokens(ctx context.Context, req *model.Request) (model.TokenCount, error) {
	stripped := messagesWithoutThinking(req.Messages)
	reqCopy := *req
	reqCopy.Messages = stripped
	prep, err := c.prepareRequest(&reqCopy)
	if err != nil {
		return model.TokenCount{}, err
	}
	resp, err := c.models.CountTokens(ctx, prep.modelID, prep.contents, &genai.CountTokensConfig{
		SystemInstruction: prep.config.SystemInstruction,
		Tools:             prep.config.Tools,
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

// messagesWithoutThinking returns a copy of msgs with ThinkingParts removed.
func messagesWithoutThinking(msgs []*model.Message) []*model.Message {
	out := make([]*model.Message, 0, len(msgs))
	for _, msg := range msgs {
		if msg == nil {
			continue
		}
		parts := make([]model.Part, 0, len(msg.Parts))
		for _, part := range msg.Parts {
			if _, isThinking := part.(model.ThinkingPart); isThinking {
				continue
			}
			parts = append(parts, part)
		}
		copied := *msg
		copied.Parts = parts
		out = append(out, &copied)
	}
	return out
}

// prepareRequest translates a model.Request into Gemini call inputs,
// applying capability gates shared by Complete, Stream, and CountTokens.
func (c *Client) prepareRequest(req *model.Request) (*preparedRequest, error) {
	if req == nil {
		return nil, errors.New("vertex: request is required")
	}
	if req.StructuredOutput != nil && len(req.Tools) > 0 {
		return nil, model.ErrStructuredOutputUnsupported
	}
	canonToProv, provToCanon := buildToolNameMaps(req.Tools)
	system, contents, err := encodeContents(req.Messages, canonToProv)
	if err != nil {
		return nil, err
	}
	if len(contents) == 0 {
		return nil, errors.New("vertex: request has no user or assistant messages")
	}
	config := &genai.GenerateContentConfig{SystemInstruction: system}
	if maxTokens := req.MaxTokens; maxTokens > 0 {
		config.MaxOutputTokens = int32(maxTokens) //nolint:gosec // genai requires int32; goa-ai request token counts fit comfortably
	} else if c.opts.MaxTokens > 0 {
		config.MaxOutputTokens = int32(c.opts.MaxTokens) //nolint:gosec // genai requires int32; goa-ai request token counts fit comfortably
	}
	temperature := req.Temperature
	if temperature == 0 {
		temperature = c.opts.Temperature
	}
	if temperature != 0 {
		config.Temperature = genai.Ptr(temperature)
	}
	if len(req.Tools) > 0 {
		tools, err := encodeTools(req.Tools, canonToProv)
		if err != nil {
			return nil, err
		}
		config.Tools = tools
		config.ToolConfig = encodeToolConfig(req.ToolChoice, canonToProv)
	}
	if req.StructuredOutput != nil {
		schema, err := normalizeSchema(req.StructuredOutput.Schema)
		if err != nil {
			return nil, err
		}
		config.ResponseMIMEType = "application/json"
		config.ResponseJsonSchema = schema
	}
	if req.Thinking != nil && req.Thinking.Enable {
		budget := req.Thinking.BudgetTokens
		if budget == 0 {
			budget = c.opts.ThinkingBudget
		}
		tc := &genai.ThinkingConfig{IncludeThoughts: true}
		if budget > 0 {
			tc.ThinkingBudget = genai.Ptr(int32(budget)) //nolint:gosec // genai requires int32; goa-ai thinking budgets fit comfortably
		}
		config.ThinkingConfig = tc
	}
	return &preparedRequest{
		modelID:          c.opts.resolveModelID(req),
		modelClass:       req.ModelClass,
		contents:         contents,
		config:           config,
		provToCanon:      provToCanon,
		structuredOutput: req.StructuredOutput,
	}, nil
}
