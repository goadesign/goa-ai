// Package gateway translates between canonical model types and the
// Goa-generated model_gateway gRPC wire types.
package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	genmodelgateway "goa.design/goa-ai/features/model/gateway/gen/model_gateway"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
	toolcontent "goa.design/goa-ai/runtime/content"
)

// Stream frame kinds and message part kinds carried inside the typed wire
// contract.
const (
	streamFrameKindChunk    = "chunk"
	streamFrameKindResponse = "response"
	streamFrameKindError    = "error"

	partKindText            = "text"
	partKindThinking        = "thinking"
	partKindImage           = "image"
	partKindImageSource     = "image_source"
	partKindDocument        = "document"
	partKindCitations       = "citations"
	partKindToolUse         = "tool_use"
	partKindToolResult      = "tool_result"
	partKindCacheCheckpoint = "cache_checkpoint"
)

// toGenRequest converts a model.Request into its Goa-generated wire representation.
func toGenRequest(req *model.Request) (*genmodelgateway.ModelRequest, error) {
	if req == nil {
		return nil, errors.New("gateway: request is nil")
	}
	out := &genmodelgateway.ModelRequest{
		Model:       req.Model,
		ModelClass:  string(req.ModelClass),
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      req.Stream,
	}
	if len(req.PromptRefs) > 0 {
		out.PromptRefs = make([]*genmodelgateway.PromptRef, len(req.PromptRefs))
		for i, ref := range req.PromptRefs {
			out.PromptRefs[i] = &genmodelgateway.PromptRef{
				ID:      string(ref.ID),
				Version: ref.Version,
			}
		}
	}
	if len(req.Messages) > 0 {
		out.Messages = make([]*genmodelgateway.ModelMessage, 0, len(req.Messages))
		for i, msg := range req.Messages {
			if msg == nil {
				continue
			}
			encoded, err := toGenMessage(*msg)
			if err != nil {
				return nil, fmt.Errorf("gateway: encode request message %d: %w", i, err)
			}
			out.Messages = append(out.Messages, encoded)
		}
	}
	if len(req.Tools) > 0 {
		out.Tools = make([]*genmodelgateway.ToolDefinition, len(req.Tools))
		for i, def := range req.Tools {
			if def == nil {
				return nil, fmt.Errorf("gateway: request tool %d is nil", i)
			}
			contract := def.Input.Contract()
			out.Tools[i] = &genmodelgateway.ToolDefinition{
				Name: def.Name,
				Search: &genmodelgateway.SearchDocument{
					Length: def.Search.Length,
					Terms:  maps.Clone(def.Search.Terms),
				},
				Deferred:    def.Deferred,
				Description: def.Description,
				Input: &genmodelgateway.ToolInputContract{
					Schema:                   append([]byte(nil), contract.Schema...),
					SchemaWithoutRootExample: append([]byte(nil), contract.SchemaWithoutRootExample...),
					ExampleJSON:              append([]byte(nil), contract.ExampleJSON...),
				},
				NoArguments: def.NoArguments,
			}
		}
	}
	if req.ToolChoice != nil {
		out.ToolChoice = &genmodelgateway.ToolChoice{
			Mode: string(req.ToolChoice.Mode),
			Name: req.ToolChoice.Name,
		}
	}
	if req.Thinking != nil {
		out.Thinking = &genmodelgateway.ThinkingOptions{
			Enable:       req.Thinking.Enable,
			Interleaved:  req.Thinking.Interleaved,
			BudgetTokens: req.Thinking.BudgetTokens,
		}
	}
	if req.StructuredOutput != nil {
		out.StructuredOutput = &genmodelgateway.StructuredOutput{
			Schema:                   append([]byte(nil), req.StructuredOutput.Schema...),
			SchemaWithoutRootExample: append([]byte(nil), req.StructuredOutput.SchemaWithoutRootExample...),
			ExampleJSON:              append([]byte(nil), req.StructuredOutput.ExampleJSON...),
			Name:                     req.StructuredOutput.Name,
			Description:              req.StructuredOutput.Description,
		}
	}
	if req.Cache != nil {
		out.Cache = &genmodelgateway.CacheOptions{
			AfterSystem: req.Cache.AfterSystem,
			AfterTools:  req.Cache.AfterTools,
		}
	}
	return out, nil
}

// fromGenRequest reconstructs a model.Request from its Goa-generated wire representation.
func fromGenRequest(in *genmodelgateway.ModelRequest) (*model.Request, error) {
	if in == nil {
		return nil, errors.New("gateway: wire request is nil")
	}
	req := &model.Request{
		Model:       in.Model,
		ModelClass:  model.ModelClass(in.ModelClass),
		Temperature: in.Temperature,
		MaxTokens:   in.MaxTokens,
		Stream:      in.Stream,
	}
	if len(in.PromptRefs) > 0 {
		req.PromptRefs = make([]prompt.PromptRef, len(in.PromptRefs))
		for i, ref := range in.PromptRefs {
			if ref == nil {
				continue
			}
			req.PromptRefs[i] = prompt.PromptRef{
				ID:      prompt.Ident(ref.ID),
				Version: ref.Version,
			}
		}
	}
	if len(in.Messages) > 0 {
		req.Messages = make([]*model.Message, 0, len(in.Messages))
		for i, gm := range in.Messages {
			if gm == nil {
				continue
			}
			msg, err := fromGenMessage(gm)
			if err != nil {
				return nil, fmt.Errorf("gateway: decode request message %d: %w", i, err)
			}
			req.Messages = append(req.Messages, &msg)
		}
	}
	if len(in.Tools) > 0 {
		req.Tools = make([]*model.ToolDefinition, len(in.Tools))
		for i, gt := range in.Tools {
			if gt == nil {
				return nil, fmt.Errorf("gateway: decoded tool %d is nil", i)
			}
			var contract model.ToolInputContract
			if gt.Input != nil {
				contract = model.ToolInputContract{
					Schema:                   rawjson.Message(append([]byte(nil), gt.Input.Schema...)),
					SchemaWithoutRootExample: rawjson.Message(append([]byte(nil), gt.Input.SchemaWithoutRootExample...)),
					ExampleJSON:              rawjson.Message(append([]byte(nil), gt.Input.ExampleJSON...)),
				}
			}
			input, err := model.ToolInputFromContract(gt.Name, contract)
			if err != nil {
				return nil, fmt.Errorf("gateway: decode tool %q input contract: %w", gt.Name, err)
			}
			var search tools.SearchDocument
			if gt.Search != nil {
				search = tools.SearchDocument{
					Length: gt.Search.Length,
					Terms:  maps.Clone(gt.Search.Terms),
				}
			}
			req.Tools[i] = &model.ToolDefinition{
				Name:        gt.Name,
				Search:      search,
				Deferred:    gt.Deferred,
				Description: gt.Description,
				Input:       input,
				NoArguments: gt.NoArguments,
			}
		}
	}
	if in.ToolChoice != nil {
		req.ToolChoice = &model.ToolChoice{
			Mode: model.ToolChoiceMode(in.ToolChoice.Mode),
			Name: in.ToolChoice.Name,
		}
	}
	if in.Thinking != nil {
		req.Thinking = &model.ThinkingOptions{
			Enable:       in.Thinking.Enable,
			Interleaved:  in.Thinking.Interleaved,
			BudgetTokens: in.Thinking.BudgetTokens,
		}
	}
	if in.StructuredOutput != nil {
		req.StructuredOutput = &model.StructuredOutput{
			Schema:                   append([]byte(nil), in.StructuredOutput.Schema...),
			SchemaWithoutRootExample: append([]byte(nil), in.StructuredOutput.SchemaWithoutRootExample...),
			ExampleJSON:              rawjson.Message(append([]byte(nil), in.StructuredOutput.ExampleJSON...)),
			Name:                     in.StructuredOutput.Name,
			Description:              in.StructuredOutput.Description,
		}
	}
	if in.Cache != nil {
		req.Cache = &model.CacheOptions{
			AfterSystem: in.Cache.AfterSystem,
			AfterTools:  in.Cache.AfterTools,
		}
	}
	return req, nil
}

// toGenResponse converts a model.Response into its Goa-generated wire representation.
func toGenResponse(resp *model.Response) (*genmodelgateway.ModelResponse, error) {
	if resp == nil {
		return nil, nil
	}
	out := &genmodelgateway.ModelResponse{
		Usage:         toGenUsage(resp.Usage),
		UsageReported: resp.UsageReported,
		StopReason:    resp.StopReason,
		OutputLimited: resp.OutputLimited,
	}
	if len(resp.Content) > 0 {
		out.Content = make([]*genmodelgateway.ModelMessage, len(resp.Content))
		for i, msg := range resp.Content {
			gm, err := toGenMessage(msg)
			if err != nil {
				return nil, fmt.Errorf("gateway: encode response message %d: %w", i, err)
			}
			out.Content[i] = gm
		}
	}
	return out, nil
}

// fromGenResponse converts a Goa-generated ModelResponse back into a model.Response.
func fromGenResponse(in *genmodelgateway.ModelResponse) (*model.Response, error) {
	if in == nil {
		return nil, nil
	}
	resp := &model.Response{
		Usage:         fromGenUsage(in.Usage),
		UsageReported: in.UsageReported,
		StopReason:    in.StopReason,
		OutputLimited: in.OutputLimited,
	}
	if len(in.Content) > 0 {
		resp.Content = make([]model.Message, len(in.Content))
		for i, gm := range in.Content {
			msg, err := fromGenMessage(gm)
			if err != nil {
				return nil, fmt.Errorf("gateway: decode response message %d: %w", i, err)
			}
			resp.Content[i] = msg
		}
	}
	return resp, nil
}

// toGenMessage converts a model.Message into its Goa-generated wire representation.
func toGenMessage(msg model.Message) (*genmodelgateway.ModelMessage, error) {
	meta, err := model.MarshalMetadata(msg.Meta)
	if err != nil {
		return nil, err
	}
	out := &genmodelgateway.ModelMessage{
		Role:     string(msg.Role),
		MetaJSON: []byte(meta),
	}
	if len(msg.Parts) > 0 {
		out.Parts = make([]*genmodelgateway.MessagePart, len(msg.Parts))
		for i, part := range msg.Parts {
			gp, err := toGenPart(part)
			if err != nil {
				return nil, fmt.Errorf("part %d: %w", i, err)
			}
			out.Parts[i] = gp
		}
	}
	return out, nil
}

// fromGenMessage converts a Goa-generated ModelMessage back into a model.Message.
func fromGenMessage(in *genmodelgateway.ModelMessage) (model.Message, error) {
	if in == nil {
		return model.Message{}, errors.New("gateway: wire message is nil")
	}
	meta, err := model.UnmarshalMetadata(rawjson.Message(in.MetaJSON))
	if err != nil {
		return model.Message{}, err
	}
	msg := model.Message{
		Role: model.ConversationRole(in.Role),
		Meta: meta,
	}
	if len(in.Parts) > 0 {
		msg.Parts = make([]model.Part, len(in.Parts))
		for i, gp := range in.Parts {
			part, err := fromGenPart(gp)
			if err != nil {
				return model.Message{}, fmt.Errorf("part %d: %w", i, err)
			}
			msg.Parts[i] = part
		}
	}
	return msg, nil
}

// toGenPart converts one model.Part into a Goa-generated MessagePart without
// rejecting raw provider fields that client-side validation will inspect.
func toGenPart(part model.Part) (*genmodelgateway.MessagePart, error) {
	switch v := part.(type) {
	case model.TextPart:
		return &genmodelgateway.MessagePart{Kind: partKindText, Text: v.Text}, nil
	case model.ThinkingPart:
		return &genmodelgateway.MessagePart{
			Kind:      partKindThinking,
			Text:      v.Text,
			Signature: v.Signature,
			Redacted:  append([]byte(nil), v.Redacted...),
			Index:     v.Index,
			Final:     v.Final,
		}, nil
	case model.ImagePart:
		return &genmodelgateway.MessagePart{
			Kind:        partKindImage,
			ImageFormat: string(v.Format),
			Bytes:       append([]byte(nil), v.Bytes...),
		}, nil
	case model.ImageSourcePart:
		return &genmodelgateway.MessagePart{
			Kind:       partKindImageSource,
			SourceKind: v.SourceKind,
			Data:       append([]byte(nil), v.Data...),
		}, nil
	case model.DocumentPart:
		return &genmodelgateway.MessagePart{
			Kind:           partKindDocument,
			Name:           v.Name,
			DocumentFormat: string(v.Format),
			Bytes:          append([]byte(nil), v.Bytes...),
			Text:           v.Text,
			Chunks:         append([]string(nil), v.Chunks...),
			URI:            v.URI,
			Context:        v.Context,
			Cite:           v.Cite,
		}, nil
	case model.CitationsPart:
		var citations []*genmodelgateway.Citation
		if len(v.Citations) > 0 {
			citations = make([]*genmodelgateway.Citation, len(v.Citations))
			for i, c := range v.Citations {
				citations[i] = toGenCitation(c)
			}
		}
		return &genmodelgateway.MessagePart{
			Kind:      partKindCitations,
			Text:      v.Text,
			Citations: citations,
		}, nil
	case model.ToolUsePart:
		return &genmodelgateway.MessagePart{
			Kind:             partKindToolUse,
			ID:               v.ID,
			Name:             v.Name,
			Input:            append([]byte(nil), v.Input...),
			ThoughtSignature: v.ThoughtSignature,
		}, nil
	case model.ToolResultPart:
		gp := &genmodelgateway.MessagePart{
			Kind:      partKindToolResult,
			ToolUseID: v.ToolUseID,
			IsError:   v.IsError,
		}
		if v.Content != nil {
			raw, err := json.Marshal(v.Content)
			if err != nil {
				return nil, fmt.Errorf("marshal tool result content: %w", err)
			}
			gp.Content = raw
			gp.ContentPresent = true
		}
		if len(v.Blocks) > 0 {
			blocksRaw, err := json.Marshal(v.Blocks)
			if err != nil {
				return nil, fmt.Errorf("marshal tool result blocks: %w", err)
			}
			gp.BlocksJSON = blocksRaw
		}
		return gp, nil
	case model.CacheCheckpointPart:
		return &genmodelgateway.MessagePart{Kind: partKindCacheCheckpoint}, nil
	default:
		return nil, fmt.Errorf("unsupported message part %T", part)
	}
}

// fromGenPart reconstructs one model.Part from its Goa-generated wire representation.
func fromGenPart(in *genmodelgateway.MessagePart) (model.Part, error) {
	if in == nil {
		return nil, errors.New("gateway: wire part is nil")
	}
	switch in.Kind {
	case partKindText:
		return model.TextPart{Text: in.Text}, nil
	case partKindThinking:
		return model.ThinkingPart{
			Text:      in.Text,
			Signature: in.Signature,
			Redacted:  append([]byte(nil), in.Redacted...),
			Index:     in.Index,
			Final:     in.Final,
		}, nil
	case partKindImage:
		return model.ImagePart{
			Format: model.ImageFormat(in.ImageFormat),
			Bytes:  append([]byte(nil), in.Bytes...),
		}, nil
	case partKindImageSource:
		return model.ImageSourcePart{
			SourceKind: in.SourceKind,
			Data:       rawjson.Message(append([]byte(nil), in.Data...)),
		}, nil
	case partKindDocument:
		return model.DocumentPart{
			Name:    in.Name,
			Format:  model.DocumentFormat(in.DocumentFormat),
			Bytes:   append([]byte(nil), in.Bytes...),
			Text:    in.Text,
			Chunks:  append([]string(nil), in.Chunks...),
			URI:     in.URI,
			Context: in.Context,
			Cite:    in.Cite,
		}, nil
	case partKindCitations:
		var citations []model.Citation
		if len(in.Citations) > 0 {
			citations = make([]model.Citation, len(in.Citations))
			for i, gc := range in.Citations {
				citations[i] = fromGenCitation(gc)
			}
		}
		return model.CitationsPart{
			Text:      in.Text,
			Citations: citations,
		}, nil
	case partKindToolUse:
		return model.ToolUsePart{
			ID:               in.ID,
			Name:             in.Name,
			Input:            rawjson.Message(append([]byte(nil), in.Input...)),
			ThoughtSignature: in.ThoughtSignature,
		}, nil
	case partKindToolResult:
		var content any
		if in.ContentPresent {
			if err := rawjson.Unmarshal(rawjson.Message(in.Content), &content); err != nil {
				return nil, fmt.Errorf("unmarshal tool result content: %w", err)
			}
		}
		var blocks toolcontent.Blocks
		if len(in.BlocksJSON) > 0 {
			if err := json.Unmarshal(in.BlocksJSON, &blocks); err != nil {
				return nil, fmt.Errorf("unmarshal tool result blocks: %w", err)
			}
		}
		return model.ToolResultPart{
			ToolUseID: in.ToolUseID,
			Content:   content,
			Blocks:    blocks,
			IsError:   in.IsError,
		}, nil
	case partKindCacheCheckpoint:
		return model.CacheCheckpointPart{}, nil
	default:
		return nil, fmt.Errorf("unsupported wire part kind %q", in.Kind)
	}
}

// toGenCitation converts a model.Citation into its Goa-generated wire representation.
func toGenCitation(c model.Citation) *genmodelgateway.Citation {
	out := &genmodelgateway.Citation{
		Title:         c.Title,
		Source:        c.Source,
		SourceContent: append([]string(nil), c.SourceContent...),
	}
	if c.Location.DocumentChar != nil || c.Location.DocumentPage != nil || c.Location.DocumentChunk != nil {
		loc := &genmodelgateway.CitationLocation{}
		if c.Location.DocumentChar != nil {
			loc.DocumentChar = &genmodelgateway.DocumentCharLocation{
				DocumentIndex: c.Location.DocumentChar.DocumentIndex,
				Start:         c.Location.DocumentChar.Start,
				End:           c.Location.DocumentChar.End,
			}
		}
		if c.Location.DocumentPage != nil {
			loc.DocumentPage = &genmodelgateway.DocumentPageLocation{
				DocumentIndex: c.Location.DocumentPage.DocumentIndex,
				Start:         c.Location.DocumentPage.Start,
				End:           c.Location.DocumentPage.End,
			}
		}
		if c.Location.DocumentChunk != nil {
			loc.DocumentChunk = &genmodelgateway.DocumentChunkLocation{
				DocumentIndex: c.Location.DocumentChunk.DocumentIndex,
				Start:         c.Location.DocumentChunk.Start,
				End:           c.Location.DocumentChunk.End,
			}
		}
		out.Location = loc
	}
	return out
}

// fromGenCitation converts a Goa-generated Citation back into a model.Citation.
func fromGenCitation(in *genmodelgateway.Citation) model.Citation {
	if in == nil {
		return model.Citation{}
	}
	out := model.Citation{
		Title:         in.Title,
		Source:        in.Source,
		SourceContent: append([]string(nil), in.SourceContent...),
	}
	if in.Location != nil {
		if in.Location.DocumentChar != nil {
			out.Location.DocumentChar = &model.DocumentCharLocation{
				DocumentIndex: in.Location.DocumentChar.DocumentIndex,
				Start:         in.Location.DocumentChar.Start,
				End:           in.Location.DocumentChar.End,
			}
		}
		if in.Location.DocumentPage != nil {
			out.Location.DocumentPage = &model.DocumentPageLocation{
				DocumentIndex: in.Location.DocumentPage.DocumentIndex,
				Start:         in.Location.DocumentPage.Start,
				End:           in.Location.DocumentPage.End,
			}
		}
		if in.Location.DocumentChunk != nil {
			out.Location.DocumentChunk = &model.DocumentChunkLocation{
				DocumentIndex: in.Location.DocumentChunk.DocumentIndex,
				Start:         in.Location.DocumentChunk.Start,
				End:           in.Location.DocumentChunk.End,
			}
		}
	}
	return out
}

// toGenChunk converts one model.Chunk into its Goa-generated wire representation.
func toGenChunk(chunk model.Chunk) (*genmodelgateway.StreamChunk, error) {
	if chunk == nil {
		return nil, errors.New("gateway: chunk is nil")
	}
	switch v := chunk.(type) {
	case model.TextChunk:
		gm, err := toGenMessage(v.Message)
		if err != nil {
			return nil, err
		}
		return &genmodelgateway.StreamChunk{Kind: model.ChunkTypeText, Message: gm}, nil
	case model.ThinkingChunk:
		gm, err := toGenMessage(v.Message)
		if err != nil {
			return nil, err
		}
		return &genmodelgateway.StreamChunk{Kind: model.ChunkTypeThinking, Message: gm}, nil
	case model.ToolCallChunk:
		return &genmodelgateway.StreamChunk{
			Kind: model.ChunkTypeToolCall,
			ToolCall: &genmodelgateway.ToolCall{
				Name:             string(v.ToolCall.Name),
				Payload:          append([]byte(nil), v.ToolCall.Payload...),
				ID:               v.ToolCall.ID,
				ThoughtSignature: v.ToolCall.ThoughtSignature,
			},
		}, nil
	case model.ToolCallDeltaChunk:
		return &genmodelgateway.StreamChunk{
			Kind: model.ChunkTypeToolCallDelta,
			ToolCallDelta: &genmodelgateway.ToolCallDelta{
				Name:  string(v.Delta.Name),
				ID:    v.Delta.ID,
				Delta: v.Delta.Delta,
			},
		}, nil
	case model.CompletionChunk:
		return &genmodelgateway.StreamChunk{
			Kind: model.ChunkTypeCompletion,
			Completion: &genmodelgateway.Completion{
				Name:    v.Completion.Name,
				Payload: append([]byte(nil), v.Completion.Payload...),
			},
		}, nil
	case model.CompletionDeltaChunk:
		return &genmodelgateway.StreamChunk{
			Kind: model.ChunkTypeCompletionDelta,
			CompletionDelta: &genmodelgateway.CompletionDelta{
				Name:  v.Delta.Name,
				Delta: v.Delta.Delta,
			},
		}, nil
	case model.UsageChunk:
		return &genmodelgateway.StreamChunk{
			Kind:  model.ChunkTypeUsage,
			Usage: toGenUsage(v.Usage),
		}, nil
	case model.StopChunk:
		return &genmodelgateway.StreamChunk{
			Kind:          model.ChunkTypeStop,
			StopReason:    v.Reason,
			OutputLimited: v.OutputLimited,
		}, nil
	default:
		return nil, fmt.Errorf("gateway: unsupported chunk %T", chunk)
	}
}

// fromGenChunk reconstructs a model.Chunk from its Goa-generated wire representation.
func fromGenChunk(in *genmodelgateway.StreamChunk) (model.Chunk, error) {
	if in == nil {
		return nil, errors.New("gateway: wire chunk is nil")
	}
	switch in.Kind {
	case model.ChunkTypeText:
		if in.Message == nil {
			return nil, errors.New("gateway: text chunk missing message")
		}
		msg, err := fromGenMessage(in.Message)
		if err != nil {
			return nil, err
		}
		return model.TextChunk{Message: msg}, nil
	case model.ChunkTypeThinking:
		if in.Message == nil {
			return nil, errors.New("gateway: thinking chunk missing message")
		}
		msg, err := fromGenMessage(in.Message)
		if err != nil {
			return nil, err
		}
		return model.ThinkingChunk{Message: msg}, nil
	case model.ChunkTypeToolCall:
		if in.ToolCall == nil {
			return nil, errors.New("gateway: tool_call chunk missing tool_call")
		}
		return model.ToolCallChunk{
			ToolCall: model.ToolCall{
				Name:             tools.Ident(in.ToolCall.Name),
				Payload:          rawjson.Message(append([]byte(nil), in.ToolCall.Payload...)),
				ID:               in.ToolCall.ID,
				ThoughtSignature: in.ToolCall.ThoughtSignature,
			},
		}, nil
	case model.ChunkTypeToolCallDelta:
		if in.ToolCallDelta == nil {
			return nil, errors.New("gateway: tool_call_delta chunk missing delta")
		}
		return model.ToolCallDeltaChunk{
			Delta: model.ToolCallDelta{
				Name:  tools.Ident(in.ToolCallDelta.Name),
				ID:    in.ToolCallDelta.ID,
				Delta: in.ToolCallDelta.Delta,
			},
		}, nil
	case model.ChunkTypeCompletion:
		if in.Completion == nil {
			return nil, errors.New("gateway: completion chunk missing completion")
		}
		return model.CompletionChunk{
			Completion: model.Completion{
				Name:    in.Completion.Name,
				Payload: rawjson.Message(append([]byte(nil), in.Completion.Payload...)),
			},
		}, nil
	case model.ChunkTypeCompletionDelta:
		if in.CompletionDelta == nil {
			return nil, errors.New("gateway: completion_delta chunk missing delta")
		}
		return model.CompletionDeltaChunk{
			Delta: model.CompletionDelta{
				Name:  in.CompletionDelta.Name,
				Delta: in.CompletionDelta.Delta,
			},
		}, nil
	case model.ChunkTypeUsage:
		if in.Usage == nil {
			return nil, errors.New("gateway: usage chunk missing usage")
		}
		return model.UsageChunk{Usage: fromGenUsage(in.Usage)}, nil
	case model.ChunkTypeStop:
		return model.StopChunk{
			Reason:        in.StopReason,
			OutputLimited: in.OutputLimited,
		}, nil
	default:
		return nil, fmt.Errorf("gateway: unsupported wire chunk kind %q", in.Kind)
	}
}

// toGenUsage converts model.TokenUsage into its Goa-generated wire representation.
func toGenUsage(u model.TokenUsage) *genmodelgateway.TokenUsage {
	return &genmodelgateway.TokenUsage{
		Model:            u.Model,
		ModelClass:       string(u.ModelClass),
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		TotalTokens:      u.TotalTokens,
		CacheReadTokens:  u.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens,
	}
}

// fromGenUsage converts a Goa-generated TokenUsage back into model.TokenUsage.
func fromGenUsage(in *genmodelgateway.TokenUsage) model.TokenUsage {
	if in == nil {
		return model.TokenUsage{}
	}
	return model.TokenUsage{
		Model:            in.Model,
		ModelClass:       model.ModelClass(in.ModelClass),
		InputTokens:      in.InputTokens,
		OutputTokens:     in.OutputTokens,
		TotalTokens:      in.TotalTokens,
		CacheReadTokens:  in.CacheReadTokens,
		CacheWriteTokens: in.CacheWriteTokens,
	}
}

// toGenTokenCount converts model.TokenCount into its Goa-generated wire representation.
func toGenTokenCount(c model.TokenCount) *genmodelgateway.TokenCount {
	return &genmodelgateway.TokenCount{
		Model:       c.Model,
		ModelClass:  string(c.ModelClass),
		InputTokens: c.InputTokens,
		Exact:       c.Exact,
	}
}

// fromGenTokenCount converts a Goa-generated TokenCount back into model.TokenCount.
func fromGenTokenCount(in *genmodelgateway.TokenCount) model.TokenCount {
	if in == nil {
		return model.TokenCount{}
	}
	return model.TokenCount{
		Model:       in.Model,
		ModelClass:  model.ModelClass(in.ModelClass),
		InputTokens: in.InputTokens,
		Exact:       in.Exact,
	}
}
