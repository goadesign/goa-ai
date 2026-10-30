// Package gateway translates between canonical model types and the
// Goa-generated model_gateway gRPC wire types.
package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"

	genmodelgateway "goa.design/goa-ai/features/model/gateway/gen/model_gateway"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
	toolcontent "goa.design/goa-ai/runtime/content"
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
		MaxTokens:   int64(req.MaxTokens),
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
			var terms map[string]int64
			if len(def.Search.Terms) > 0 {
				terms = make(map[string]int64, len(def.Search.Terms))
				for k, v := range def.Search.Terms {
					terms[k] = int64(v)
				}
			}
			out.Tools[i] = &genmodelgateway.ToolDefinition{
				Name: def.Name,
				Search: &genmodelgateway.SearchDocument{
					Length: int64(def.Search.Length),
					Terms:  terms,
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
			BudgetTokens: int64(req.Thinking.BudgetTokens),
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
	maxTokens, err := int64ToInt(in.MaxTokens, "max_tokens")
	if err != nil {
		return nil, err
	}
	req := &model.Request{
		Model:       in.Model,
		ModelClass:  model.ModelClass(in.ModelClass),
		Temperature: in.Temperature,
		MaxTokens:   maxTokens,
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
				searchLen, err := int64ToInt(gt.Search.Length, "search.length")
				if err != nil {
					return nil, err
				}
				var terms map[string]int
				if len(gt.Search.Terms) > 0 {
					terms = make(map[string]int, len(gt.Search.Terms))
					for k, v := range gt.Search.Terms {
						tv, err := int64ToInt(v, "search.terms")
						if err != nil {
							return nil, err
						}
						terms[k] = tv
					}
				}
				search = tools.SearchDocument{
					Length: searchLen,
					Terms:  terms,
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
		budget, err := int64ToInt(in.Thinking.BudgetTokens, "thinking.budget_tokens")
		if err != nil {
			return nil, err
		}
		req.Thinking = &model.ThinkingOptions{
			Enable:       in.Thinking.Enable,
			Interleaved:  in.Thinking.Interleaved,
			BudgetTokens: budget,
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
		Usage:           toGenUsage(resp.Usage),
		UsageReported:   resp.UsageReported,
		UsageIncomplete: resp.UsageIncomplete,
		StopReason:      resp.StopReason,
		OutputLimited:   resp.OutputLimited,
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
	usage, err := fromGenUsage(in.Usage)
	if err != nil {
		return nil, err
	}
	resp := &model.Response{
		Usage:           usage,
		UsageReported:   in.UsageReported,
		UsageIncomplete: in.UsageIncomplete,
		StopReason:      in.StopReason,
		OutputLimited:   in.OutputLimited,
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
		return &genmodelgateway.MessagePart{
			Payload: genmodelgateway.NewMessagePartPayloadText(&genmodelgateway.TextPartDetail{
				Text: v.Text,
			}),
		}, nil
	case model.ThinkingPart:
		return &genmodelgateway.MessagePart{
			Payload: genmodelgateway.NewMessagePartPayloadThinking(&genmodelgateway.ThinkingPartDetail{
				Text:      v.Text,
				Signature: v.Signature,
				Redacted:  append([]byte(nil), v.Redacted...),
				Index:     int64(v.Index),
				Final:     v.Final,
			}),
		}, nil
	case model.ImagePart:
		return &genmodelgateway.MessagePart{
			Payload: genmodelgateway.NewMessagePartPayloadImage(&genmodelgateway.ImagePartDetail{
				Format: string(v.Format),
				Bytes:  append([]byte(nil), v.Bytes...),
			}),
		}, nil
	case model.ImageSourcePart:
		return &genmodelgateway.MessagePart{
			Payload: genmodelgateway.NewMessagePartPayloadImageSource(&genmodelgateway.ImageSourcePartDetail{
				SourceKind: v.SourceKind,
				Data:       append([]byte(nil), v.Data...),
			}),
		}, nil
	case model.DocumentPart:
		return &genmodelgateway.MessagePart{
			Payload: genmodelgateway.NewMessagePartPayloadDocument(&genmodelgateway.DocumentPartDetail{
				Name:    v.Name,
				Format:  string(v.Format),
				Bytes:   append([]byte(nil), v.Bytes...),
				Text:    v.Text,
				Chunks:  append([]string(nil), v.Chunks...),
				URI:     v.URI,
				Context: v.Context,
				Cite:    v.Cite,
			}),
		}, nil
	case model.CitationsPart:
		var citations []*genmodelgateway.Citation
		if len(v.Citations) > 0 {
			citations = make([]*genmodelgateway.Citation, len(v.Citations))
			for i, c := range v.Citations {
				gc, err := toGenCitation(c)
				if err != nil {
					return nil, err
				}
				citations[i] = gc
			}
		}
		return &genmodelgateway.MessagePart{
			Payload: genmodelgateway.NewMessagePartPayloadCitations(&genmodelgateway.CitationsPartDetail{
				Text:      v.Text,
				Citations: citations,
			}),
		}, nil
	case model.ToolUsePart:
		return &genmodelgateway.MessagePart{
			Payload: genmodelgateway.NewMessagePartPayloadToolUse(&genmodelgateway.ToolUsePartDetail{
				ID:               v.ID,
				Name:             v.Name,
				Input:            append([]byte(nil), v.Input...),
				ThoughtSignature: v.ThoughtSignature,
			}),
		}, nil
	case model.ToolResultPart:
		detail := &genmodelgateway.ToolResultPartDetail{
			ToolUseID: v.ToolUseID,
			IsError:   v.IsError,
		}
		if v.Content != nil {
			raw, err := json.Marshal(v.Content)
			if err != nil {
				return nil, fmt.Errorf("marshal tool result content: %w", err)
			}
			detail.Content = raw
			detail.ContentPresent = true
		}
		if len(v.Blocks) > 0 {
			blocksRaw, err := json.Marshal(v.Blocks)
			if err != nil {
				return nil, fmt.Errorf("marshal tool result blocks: %w", err)
			}
			detail.BlocksJSON = blocksRaw
		}
		return &genmodelgateway.MessagePart{
			Payload: genmodelgateway.NewMessagePartPayloadToolResult(detail),
		}, nil
	case model.CacheCheckpointPart:
		return &genmodelgateway.MessagePart{
			Payload: genmodelgateway.NewMessagePartPayloadCacheCheckpoint(&genmodelgateway.CacheCheckpointDetail{}),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported message part %T", part)
	}
}

// fromGenPart reconstructs one model.Part from its Goa-generated wire representation.
func fromGenPart(in *genmodelgateway.MessagePart) (model.Part, error) {
	if in == nil {
		return nil, errors.New("gateway: wire part is nil")
	}
	if err := in.Payload.Validate(); err != nil {
		return nil, fmt.Errorf("gateway: invalid message part payload: %w", err)
	}
	switch in.Payload.Kind() {
	case genmodelgateway.MessagePartPayloadKindText:
		v, _ := in.Payload.AsText()
		return model.TextPart{Text: v.Text}, nil
	case genmodelgateway.MessagePartPayloadKindThinking:
		v, _ := in.Payload.AsThinking()
		idx, err := int64ToInt(v.Index, "thinking_part.index")
		if err != nil {
			return nil, err
		}
		return model.ThinkingPart{
			Text:      v.Text,
			Signature: v.Signature,
			Redacted:  append([]byte(nil), v.Redacted...),
			Index:     idx,
			Final:     v.Final,
		}, nil
	case genmodelgateway.MessagePartPayloadKindImage:
		v, _ := in.Payload.AsImage()
		return model.ImagePart{
			Format: model.ImageFormat(v.Format),
			Bytes:  append([]byte(nil), v.Bytes...),
		}, nil
	case genmodelgateway.MessagePartPayloadKindImageSource:
		v, _ := in.Payload.AsImageSource()
		return model.ImageSourcePart{
			SourceKind: v.SourceKind,
			Data:       rawjson.Message(append([]byte(nil), v.Data...)),
		}, nil
	case genmodelgateway.MessagePartPayloadKindDocument:
		v, _ := in.Payload.AsDocument()
		return model.DocumentPart{
			Name:    v.Name,
			Format:  model.DocumentFormat(v.Format),
			Bytes:   append([]byte(nil), v.Bytes...),
			Text:    v.Text,
			Chunks:  append([]string(nil), v.Chunks...),
			URI:     v.URI,
			Context: v.Context,
			Cite:    v.Cite,
		}, nil
	case genmodelgateway.MessagePartPayloadKindCitations:
		v, _ := in.Payload.AsCitations()
		var citations []model.Citation
		if len(v.Citations) > 0 {
			citations = make([]model.Citation, len(v.Citations))
			for i, gc := range v.Citations {
				c, err := fromGenCitation(gc)
				if err != nil {
					return nil, err
				}
				citations[i] = c
			}
		}
		return model.CitationsPart{
			Text:      v.Text,
			Citations: citations,
		}, nil
	case genmodelgateway.MessagePartPayloadKindToolUse:
		v, _ := in.Payload.AsToolUse()
		return model.ToolUsePart{
			ID:               v.ID,
			Name:             v.Name,
			Input:            rawjson.Message(append([]byte(nil), v.Input...)),
			ThoughtSignature: v.ThoughtSignature,
		}, nil
	case genmodelgateway.MessagePartPayloadKindToolResult:
		v, _ := in.Payload.AsToolResult()
		var content any
		if v.ContentPresent {
			if err := rawjson.Unmarshal(rawjson.Message(v.Content), &content); err != nil {
				return nil, fmt.Errorf("unmarshal tool result content: %w", err)
			}
		}
		var blocks toolcontent.Blocks
		if len(v.BlocksJSON) > 0 {
			if err := json.Unmarshal(v.BlocksJSON, &blocks); err != nil {
				return nil, fmt.Errorf("unmarshal tool result blocks: %w", err)
			}
		}
		return model.ToolResultPart{
			ToolUseID: v.ToolUseID,
			Content:   content,
			Blocks:    blocks,
			IsError:   v.IsError,
		}, nil
	case genmodelgateway.MessagePartPayloadKindCacheCheckpoint:
		return model.CacheCheckpointPart{}, nil
	default:
		return nil, fmt.Errorf("unsupported wire part kind %q", in.Payload.Kind())
	}
}

// toGenCitation converts a model.Citation into its Goa-generated wire representation.
func toGenCitation(c model.Citation) (*genmodelgateway.Citation, error) {
	out := &genmodelgateway.Citation{
		Title:         c.Title,
		Source:        c.Source,
		SourceContent: append([]string(nil), c.SourceContent...),
	}
	count := 0
	if c.Location.DocumentChar != nil {
		count++
	}
	if c.Location.DocumentPage != nil {
		count++
	}
	if c.Location.DocumentChunk != nil {
		count++
	}
	if count > 1 {
		return nil, errors.New("gateway: citation location must specify at most one span")
	}
	switch {
	case c.Location.DocumentChar != nil:
		out.Location = &genmodelgateway.CitationLocation{
			Span: genmodelgateway.NewCitationSpanDocumentChar(&genmodelgateway.DocumentCharLocation{
				DocumentIndex: int64(c.Location.DocumentChar.DocumentIndex),
				Start:         int64(c.Location.DocumentChar.Start),
				End:           int64(c.Location.DocumentChar.End),
			}),
		}
	case c.Location.DocumentPage != nil:
		out.Location = &genmodelgateway.CitationLocation{
			Span: genmodelgateway.NewCitationSpanDocumentPage(&genmodelgateway.DocumentPageLocation{
				DocumentIndex: int64(c.Location.DocumentPage.DocumentIndex),
				Start:         int64(c.Location.DocumentPage.Start),
				End:           int64(c.Location.DocumentPage.End),
			}),
		}
	case c.Location.DocumentChunk != nil:
		out.Location = &genmodelgateway.CitationLocation{
			Span: genmodelgateway.NewCitationSpanDocumentChunk(&genmodelgateway.DocumentChunkLocation{
				DocumentIndex: int64(c.Location.DocumentChunk.DocumentIndex),
				Start:         int64(c.Location.DocumentChunk.Start),
				End:           int64(c.Location.DocumentChunk.End),
			}),
		}
	}
	return out, nil
}

// fromGenCitation converts a Goa-generated Citation back into a model.Citation.
func fromGenCitation(in *genmodelgateway.Citation) (model.Citation, error) {
	if in == nil {
		return model.Citation{}, errors.New("gateway: wire citation is nil")
	}
	out := model.Citation{
		Title:         in.Title,
		Source:        in.Source,
		SourceContent: append([]string(nil), in.SourceContent...),
	}
	if in.Location == nil {
		return out, nil
	}
	if err := in.Location.Span.Validate(); err != nil {
		return model.Citation{}, fmt.Errorf("gateway: invalid citation span: %w", err)
	}
	switch in.Location.Span.Kind() {
	case genmodelgateway.CitationSpanKindDocumentChar:
		v, _ := in.Location.Span.AsDocumentChar()
		docIdx, err := int64ToInt(v.DocumentIndex, "citation.document_char.document_index")
		if err != nil {
			return model.Citation{}, err
		}
		start, err := int64ToInt(v.Start, "citation.document_char.start")
		if err != nil {
			return model.Citation{}, err
		}
		end, err := int64ToInt(v.End, "citation.document_char.end")
		if err != nil {
			return model.Citation{}, err
		}
		out.Location.DocumentChar = &model.DocumentCharLocation{
			DocumentIndex: docIdx,
			Start:         start,
			End:           end,
		}
	case genmodelgateway.CitationSpanKindDocumentPage:
		v, _ := in.Location.Span.AsDocumentPage()
		docIdx, err := int64ToInt(v.DocumentIndex, "citation.document_page.document_index")
		if err != nil {
			return model.Citation{}, err
		}
		start, err := int64ToInt(v.Start, "citation.document_page.start")
		if err != nil {
			return model.Citation{}, err
		}
		end, err := int64ToInt(v.End, "citation.document_page.end")
		if err != nil {
			return model.Citation{}, err
		}
		out.Location.DocumentPage = &model.DocumentPageLocation{
			DocumentIndex: docIdx,
			Start:         start,
			End:           end,
		}
	case genmodelgateway.CitationSpanKindDocumentChunk:
		v, _ := in.Location.Span.AsDocumentChunk()
		docIdx, err := int64ToInt(v.DocumentIndex, "citation.document_chunk.document_index")
		if err != nil {
			return model.Citation{}, err
		}
		start, err := int64ToInt(v.Start, "citation.document_chunk.start")
		if err != nil {
			return model.Citation{}, err
		}
		end, err := int64ToInt(v.End, "citation.document_chunk.end")
		if err != nil {
			return model.Citation{}, err
		}
		out.Location.DocumentChunk = &model.DocumentChunkLocation{
			DocumentIndex: docIdx,
			Start:         start,
			End:           end,
		}
	default:
		return model.Citation{}, fmt.Errorf("unsupported citation span kind %q", in.Location.Span.Kind())
	}
	return out, nil
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
		return &genmodelgateway.StreamChunk{
			Payload: genmodelgateway.NewStreamChunkPayloadText(gm),
		}, nil
	case model.ThinkingChunk:
		gm, err := toGenMessage(v.Message)
		if err != nil {
			return nil, err
		}
		return &genmodelgateway.StreamChunk{
			Payload: genmodelgateway.NewStreamChunkPayloadThinking(gm),
		}, nil
	case model.ToolCallChunk:
		return &genmodelgateway.StreamChunk{
			Payload: genmodelgateway.NewStreamChunkPayloadToolCall(&genmodelgateway.ToolCall{
				Name:             string(v.ToolCall.Name),
				Payload:          append([]byte(nil), v.ToolCall.Payload...),
				ID:               v.ToolCall.ID,
				ThoughtSignature: v.ToolCall.ThoughtSignature,
			}),
		}, nil
	case model.ToolCallDeltaChunk:
		return &genmodelgateway.StreamChunk{
			Payload: genmodelgateway.NewStreamChunkPayloadToolCallDelta(&genmodelgateway.ToolCallDelta{
				Name:  string(v.Delta.Name),
				ID:    v.Delta.ID,
				Delta: v.Delta.Delta,
			}),
		}, nil
	case model.CompletionChunk:
		return &genmodelgateway.StreamChunk{
			Payload: genmodelgateway.NewStreamChunkPayloadCompletion(&genmodelgateway.Completion{
				Name:    v.Completion.Name,
				Payload: append([]byte(nil), v.Completion.Payload...),
			}),
		}, nil
	case model.CompletionDeltaChunk:
		return &genmodelgateway.StreamChunk{
			Payload: genmodelgateway.NewStreamChunkPayloadCompletionDelta(&genmodelgateway.CompletionDelta{
				Name:  v.Delta.Name,
				Delta: v.Delta.Delta,
			}),
		}, nil
	case model.UsageChunk:
		return &genmodelgateway.StreamChunk{
			Payload: genmodelgateway.NewStreamChunkPayloadUsage(toGenUsage(v.Usage)),
		}, nil
	case model.StopChunk:
		return &genmodelgateway.StreamChunk{
			Payload: genmodelgateway.NewStreamChunkPayloadStop(&genmodelgateway.StopDetail{
				Reason:        v.Reason,
				OutputLimited: v.OutputLimited,
			}),
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
	if err := in.Payload.Validate(); err != nil {
		return nil, fmt.Errorf("gateway: invalid stream chunk payload: %w", err)
	}
	switch in.Payload.Kind() {
	case genmodelgateway.StreamChunkPayloadKindText:
		v, _ := in.Payload.AsText()
		msg, err := fromGenMessage(v)
		if err != nil {
			return nil, err
		}
		return model.TextChunk{Message: msg}, nil
	case genmodelgateway.StreamChunkPayloadKindThinking:
		v, _ := in.Payload.AsThinking()
		msg, err := fromGenMessage(v)
		if err != nil {
			return nil, err
		}
		return model.ThinkingChunk{Message: msg}, nil
	case genmodelgateway.StreamChunkPayloadKindToolCall:
		v, _ := in.Payload.AsToolCall()
		return model.ToolCallChunk{
			ToolCall: model.ToolCall{
				Name:             tools.Ident(v.Name),
				Payload:          rawjson.Message(append([]byte(nil), v.Payload...)),
				ID:               v.ID,
				ThoughtSignature: v.ThoughtSignature,
			},
		}, nil
	case genmodelgateway.StreamChunkPayloadKindToolCallDelta:
		v, _ := in.Payload.AsToolCallDelta()
		return model.ToolCallDeltaChunk{
			Delta: model.ToolCallDelta{
				Name:  tools.Ident(v.Name),
				ID:    v.ID,
				Delta: v.Delta,
			},
		}, nil
	case genmodelgateway.StreamChunkPayloadKindCompletion:
		v, _ := in.Payload.AsCompletion()
		return model.CompletionChunk{
			Completion: model.Completion{
				Name:    v.Name,
				Payload: rawjson.Message(append([]byte(nil), v.Payload...)),
			},
		}, nil
	case genmodelgateway.StreamChunkPayloadKindCompletionDelta:
		v, _ := in.Payload.AsCompletionDelta()
		return model.CompletionDeltaChunk{
			Delta: model.CompletionDelta{
				Name:  v.Name,
				Delta: v.Delta,
			},
		}, nil
	case genmodelgateway.StreamChunkPayloadKindUsage:
		v, _ := in.Payload.AsUsage()
		usage, err := fromGenUsage(v)
		if err != nil {
			return nil, err
		}
		return model.UsageChunk{Usage: usage}, nil
	case genmodelgateway.StreamChunkPayloadKindStop:
		v, _ := in.Payload.AsStop()
		return model.StopChunk{
			Reason:        v.Reason,
			OutputLimited: v.OutputLimited,
		}, nil
	default:
		return nil, fmt.Errorf("gateway: unsupported wire chunk kind %q", in.Payload.Kind())
	}
}

// toGenUsage converts model.TokenUsage into its Goa-generated wire representation.
func toGenUsage(u model.TokenUsage) *genmodelgateway.TokenUsage {
	return &genmodelgateway.TokenUsage{
		Model:            u.Model,
		ModelClass:       string(u.ModelClass),
		InputTokens:      int64(u.InputTokens),
		OutputTokens:     int64(u.OutputTokens),
		TotalTokens:      int64(u.TotalTokens),
		CacheReadTokens:  int64(u.CacheReadTokens),
		CacheWriteTokens: int64(u.CacheWriteTokens),
	}
}

// fromGenUsage converts a Goa-generated TokenUsage back into model.TokenUsage.
func fromGenUsage(in *genmodelgateway.TokenUsage) (model.TokenUsage, error) {
	if in == nil {
		return model.TokenUsage{}, nil
	}
	inTokens, err := int64ToInt(in.InputTokens, "usage.input_tokens")
	if err != nil {
		return model.TokenUsage{}, err
	}
	outTokens, err := int64ToInt(in.OutputTokens, "usage.output_tokens")
	if err != nil {
		return model.TokenUsage{}, err
	}
	totalTokens, err := int64ToInt(in.TotalTokens, "usage.total_tokens")
	if err != nil {
		return model.TokenUsage{}, err
	}
	cacheRead, err := int64ToInt(in.CacheReadTokens, "usage.cache_read_tokens")
	if err != nil {
		return model.TokenUsage{}, err
	}
	cacheWrite, err := int64ToInt(in.CacheWriteTokens, "usage.cache_write_tokens")
	if err != nil {
		return model.TokenUsage{}, err
	}
	return model.TokenUsage{
		Model:            in.Model,
		ModelClass:       model.ModelClass(in.ModelClass),
		InputTokens:      inTokens,
		OutputTokens:     outTokens,
		TotalTokens:      totalTokens,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
	}, nil
}

// toGenTokenCount converts model.TokenCount into its Goa-generated wire representation.
func toGenTokenCount(c model.TokenCount) *genmodelgateway.TokenCount {
	return &genmodelgateway.TokenCount{
		Model:       c.Model,
		ModelClass:  string(c.ModelClass),
		InputTokens: int64(c.InputTokens),
		Exact:       c.Exact,
	}
}

// fromGenTokenCount converts a Goa-generated TokenCount back into model.TokenCount.
func fromGenTokenCount(in *genmodelgateway.TokenCount) (model.TokenCount, error) {
	if in == nil {
		return model.TokenCount{}, nil
	}
	inTokens, err := int64ToInt(in.InputTokens, "token_count.input_tokens")
	if err != nil {
		return model.TokenCount{}, err
	}
	return model.TokenCount{
		Model:       in.Model,
		ModelClass:  model.ModelClass(in.ModelClass),
		InputTokens: inTokens,
		Exact:       in.Exact,
	}, nil
}

// int64ToInt converts a 64-bit wire integer to Go int while rejecting values
// outside the platform's int range.
func int64ToInt(v int64, field string) (int, error) {
	if v < int64(math.MinInt) || v > int64(math.MaxInt) {
		return 0, fmt.Errorf("gateway: integer field %s (%d) overflows int", field, v)
	}
	return int(v), nil
}
