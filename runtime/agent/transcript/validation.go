package transcript

import (
	"fmt"

	"goa.design/goa-ai/runtime/agent/model"
)

// ValidatePlannerTranscript verifies the shared tool-loop invariants required by
// the runtime before it resumes planning:
//   - Assistant messages that declare tool_use must be followed immediately by a
//     user tool_result message.
//   - That user message must contain exactly one tool_result for each tool_use
//     ID declared by the immediately preceding assistant message.
func ValidatePlannerTranscript(messages []*model.Message) error {
	if len(messages) == 0 {
		return nil
	}
	for i, msg := range messages {
		if msg == nil || msg.Role != model.ConversationRoleAssistant || !messageHasToolUse(msg) {
			continue
		}
		nextIndex := i + 1
		tail := messages[nextIndex:]
		if len(tail) == 0 {
			return fmt.Errorf(
				"transcript: assistant message[%d] with tool_use must be followed by user tool_result",
				i,
			)
		}
		next := tail[0]
		if next == nil || next.Role != model.ConversationRoleUser {
			return fmt.Errorf(
				"transcript: assistant message[%d] with tool_use must be followed by user tool_result",
				i,
			)
		}
		useCount, useIDs := toolUseIDs(msg.Parts)
		if useCount == 0 {
			return fmt.Errorf(
				"transcript: assistant message[%d] tool_use ids must be non-empty",
				i,
			)
		}
		if len(useIDs) != useCount {
			return fmt.Errorf(
				"transcript: assistant message[%d] tool_use ids must be non-empty and unique",
				i,
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
	}
	return nil
}

// ValidateBedrock verifies Bedrock-specific representability constraints on top
// of the shared planner transcript invariants.
func ValidateBedrock(messages []*model.Message, thinkingEnabled bool) error {
	if err := ValidatePlannerTranscript(messages); err != nil {
		return fmt.Errorf("bedrock: %w", err)
	}
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
