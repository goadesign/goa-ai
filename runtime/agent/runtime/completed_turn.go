package runtime

import (
	"goa.design/goa-ai/internal/modelmetadata"
	"goa.design/goa-ai/runtime/agent/model"
)

// completedTurnMessages copies completed conversation history for a new user
// turn without carrying earlier private reasoning into that turn. It removes
// ThinkingParts, native reasoning metadata, and opaque tool thought signatures.
// All other parts, tool identities and arguments, citations, and metadata are
// preserved. The input messages are never changed or aliased by the result.
// Messages containing nothing but removed reasoning are omitted.
//
// The caller must establish that this history belongs to completed turns. Never
// use this operation to resume an unfinished tool exchange, confirmation, or
// suspended turn: those must replay their native continuation data unchanged.
// This is an explicit context policy, not a repair for unsupported provider
// input. It does not make other unsupported parts representable by a provider.
func completedTurnMessages(messages []*model.Message) ([]*model.Message, error) {
	cloned, err := model.CloneMessages(messages)
	if err != nil {
		return nil, err
	}
	out := cloned[:0]
	for _, message := range cloned {
		parts := message.Parts[:0]
		for _, part := range message.Parts {
			switch actual := part.(type) {
			case model.ThinkingPart:
				continue
			case model.ToolUsePart:
				actual.ThoughtSignature = ""
				part = actual
			}
			parts = append(parts, part)
		}
		clear(message.Parts[len(parts):])
		message.Parts = parts
		delete(message.Meta, modelmetadata.OpenAIReasoningItems)
		if len(message.Parts) > 0 || len(message.Meta) > 0 {
			out = append(out, message)
		}
	}
	clear(cloned[len(out):])
	return out, nil
}
