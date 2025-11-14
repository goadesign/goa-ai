package model

// TranscriptEntry represents a single ordered entry in a flattened transcript.
// Applications that persist a run's history can rebuild Messages by mapping
// each entry to a Message with the same role and parts. This helper provides
// a minimal, opinionated constructor to do that cleanly.
//
// Typical usage:
//
//	msgs := BuildMessagesFromTranscript([]TranscriptEntry{
//	    {Role: ConversationRoleSystem, Parts: []Part{TextPart{Text: sys}}},
//	    {Role: ConversationRoleUser, Parts: []Part{TextPart{Text: user}}},
//	    {Role: ConversationRoleAssistant, Parts: []Part{
//	        ThinkingPart{Text: "...", Signature: "sig"},
//	        ToolUsePart{ID: "tu1", Name: "search", Input: map[string]any{"q": "abc"}},
//	    }},
//	    {Role: ConversationRoleUser, Parts: []Part{
//	        ToolResultPart{ToolUseID: "tu1", Content: map[string]any{"items": []any{}}},
//	    }},
//	})
type TranscriptEntry struct {
	Role  ConversationRole
	Parts []Part
}

// BuildMessagesFromTranscript constructs Messages from a flat transcript.
// It preserves the provided order and parts without synthesis or normalization.
// Callers are responsible for provider-specific invariants (e.g., place
// ThinkingPart before ToolUsePart in an assistant message when tools are used).
func BuildMessagesFromTranscript(entries []TranscriptEntry) []*Message {
	if len(entries) == 0 {
		return nil
	}
	out := make([]*Message, 0, len(entries))
	for _, e := range entries {
		// Skip empty roles to keep messages meaningful.
		if e.Role == "" {
			continue
		}
		msg := &Message{
			Role:  e.Role,
			Parts: make([]Part, 0, len(e.Parts)),
			Meta:  nil,
		}
		for _, p := range e.Parts {
			// Only accept known Part implementations; marker interface ensures type safety.
			switch v := p.(type) {
			case TextPart:
				msg.Parts = append(msg.Parts, v)
			case ThinkingPart:
				msg.Parts = append(msg.Parts, v)
			case ToolUsePart:
				msg.Parts = append(msg.Parts, v)
			case ToolResultPart:
				msg.Parts = append(msg.Parts, v)
			default:
				// Ignore unknown parts; prefer explicitness.
				continue
			}
		}
		if len(msg.Parts) == 0 {
			continue
		}
		out = append(out, msg)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}


