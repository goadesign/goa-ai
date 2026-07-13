package transcript

import (
	"fmt"
	"strings"

	"goa.design/goa-ai/runtime/agent/model"
)

// ValidatePlannerTranscript verifies the shared tool-loop invariants required by
// the runtime before it resumes planning:
//   - A contiguous assistant response containing tool_use parts must be followed
//     immediately by one user tool_result message.
//   - That user message must contain exactly one tool_result for each tool_use
//     ID declared by the complete assistant response.
func ValidatePlannerTranscript(messages []*model.Message) error {
	if len(messages) == 0 {
		return nil
	}
	for i := 0; i < len(messages); {
		msg := messages[i]
		if msg == nil {
			return fmt.Errorf("transcript: message[%d] is nil", i)
		}
		if msg.Role != model.ConversationRoleAssistant {
			if resultCount, _ := toolResultIDs(msg.Parts); resultCount > 0 {
				return fmt.Errorf(
					"transcript: message[%d] role %q has tool_result without prior assistant tool_use",
					i,
					msg.Role,
				)
			}
			i++
			continue
		}
		groupEnd := i
		var assistantParts []model.Part
		for groupEnd < len(messages) {
			current := messages[groupEnd]
			if current == nil || current.Role != model.ConversationRoleAssistant {
				break
			}
			assistantParts = append(assistantParts, current.Parts...)
			groupEnd++
		}
		useCount, useIDs := toolUseIDs(assistantParts)
		if useCount == 0 {
			i = groupEnd
			continue
		}
		if groupEnd == len(messages) {
			return fmt.Errorf(
				"transcript: assistant response messages[%d:%d] with tool_use must be followed by user tool_result",
				i,
				groupEnd,
			)
		}
		nextIndex := groupEnd
		next := messages[nextIndex]
		if next == nil || next.Role != model.ConversationRoleUser {
			return fmt.Errorf(
				"transcript: assistant response messages[%d:%d] with tool_use must be followed by user tool_result",
				i,
				groupEnd,
			)
		}
		if len(useIDs) != useCount {
			return fmt.Errorf(
				"transcript: assistant response messages[%d:%d] tool_use ids must be non-empty and unique",
				i,
				groupEnd,
			)
		}
		resultCount, resultIDs := toolResultIDs(next.Parts)
		if resultCount == 0 {
			return fmt.Errorf(
				"transcript: user message[%d] must contain tool_result for prior assistant tool_use",
				nextIndex,
			)
		}
		if resultCount != useCount {
			return fmt.Errorf(
				"transcript: user message[%d] must contain exactly %d tool_result parts for prior assistant tool_use, got %d",
				nextIndex,
				useCount,
				resultCount,
			)
		}
		if len(resultIDs) != resultCount {
			return fmt.Errorf(
				"transcript: user message[%d] tool_result ids must be non-empty and unique",
				nextIndex,
			)
		}
		for id := range useIDs {
			if _, ok := resultIDs[id]; !ok {
				return fmt.Errorf(
					"transcript: user message[%d] missing tool_result id %q for prior assistant tool_use",
					nextIndex,
					id,
				)
			}
		}
		for id := range resultIDs {
			if _, ok := useIDs[id]; !ok {
				return fmt.Errorf(
					"transcript: user message[%d] tool_result id %q does not match prior assistant tool_use id",
					nextIndex,
					id,
				)
			}
		}
		i = nextIndex + 1
	}
	return nil
}

// ValidateBedrock verifies Bedrock-specific representability constraints.
// Callers own canonical transcript validation before they invoke provider
// adapters.
func ValidateBedrock(messages []*model.Message, thinkingEnabled bool) error {
	if !thinkingEnabled {
		return nil
	}
	for i, msg := range messages {
		if msg == nil || msg.Role != model.ConversationRoleAssistant || !messageHasToolUse(msg) {
			continue
		}
		if len(msg.Parts) == 0 {
			return fmt.Errorf("bedrock: assistant message[%d] is empty where tool_use present", i)
		}
		if _, ok := msg.Parts[0].(model.ThinkingPart); !ok {
			return fmt.Errorf(
				"bedrock: assistant message[%d] with tool_use must start with thinking (parts: %s)",
				i,
				summarizeParts(msg.Parts),
			)
		}
	}
	return nil
}

// messageHasToolUse reports whether the message declares any tool_use parts.
func messageHasToolUse(msg *model.Message) bool {
	if msg == nil {
		return false
	}
	for _, part := range msg.Parts {
		if _, ok := part.(model.ToolUsePart); ok {
			return true
		}
	}
	return false
}

// toolUseIDs returns the number of tool_use parts plus their distinct IDs.
func toolUseIDs(parts []model.Part) (int, map[string]struct{}) {
	ids := make(map[string]struct{})
	count := 0
	for _, part := range parts {
		use, ok := part.(model.ToolUsePart)
		if !ok {
			continue
		}
		count++
		if use.ID == "" {
			continue
		}
		ids[use.ID] = struct{}{}
	}
	return count, ids
}

// toolResultIDs returns the number of tool_result parts plus the distinct IDs
// they reference.
func toolResultIDs(parts []model.Part) (int, map[string]struct{}) {
	ids := make(map[string]struct{})
	count := 0
	for _, part := range parts {
		result, ok := part.(model.ToolResultPart)
		if !ok {
			continue
		}
		count++
		if result.ToolUseID != "" {
			ids[result.ToolUseID] = struct{}{}
		}
	}
	return count, ids
}

// summarizeParts returns provider-neutral part names for validation errors.
func summarizeParts(parts []model.Part) string {
	names := make([]string, len(parts))
	for i, part := range parts {
		switch part.(type) {
		case model.ThinkingPart:
			names[i] = "thinking"
		case model.TextPart:
			names[i] = "text"
		case model.CitationsPart:
			names[i] = "citations"
		case model.ToolUsePart:
			names[i] = "tool_use"
		case model.ToolResultPart:
			names[i] = "tool_result"
		default:
			names[i] = fmt.Sprintf("%T", part)
		}
	}
	return "[" + strings.Join(names, ", ") + "]"
}
