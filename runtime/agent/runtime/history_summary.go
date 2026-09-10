package runtime

// This file reconstructs the exact request described by a reusable history
// summary. Compression and activity admission use the same complete-turn checks
// and message selection, so a policy cannot claim coverage by approximate text
// matching or remove an instruction while describing a different prefix.

import (
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/model"
)

// historySummaryMessages checks a policy's source and replacement lengths
// against the original request. Both must end after complete older turns;
// newest is never summarized. Every original System message keeps its contents
// and its order among the retained messages. The summary follows leading
// instructions, exactly as it does for a newly generated summary.
func historySummaryMessages(messages []*model.Message, summary *HistorySummary) ([]*model.Message, error) {
	if summary.SourceMessages <= 0 || summary.ReplacedMessages <= 0 ||
		summary.ReplacedMessages > summary.SourceMessages {
		return nil, errors.New("runtime: history summary requires positive source and replaced prefix lengths with replaced <= source")
	}
	if summary.PolicyFingerprint == "" {
		return nil, errors.New("runtime: history summary requires a policy fingerprint")
	}
	systemEnd := systemPrefixEnd(messages)
	turns := parseTurns(messages[systemEnd:])
	covered, keepStart := false, -1
	count := 0
	for i := 0; i < len(turns)-1; i++ {
		count += historyConversationCount(turns[i : i+1])
		if count == summary.SourceMessages {
			covered = true
		}
		if count == summary.ReplacedMessages {
			keepStart = i + 1
		}
	}
	if !covered || keepStart < 0 {
		return nil, fmt.Errorf(
			"runtime: history summary source (%d) and replaced prefix (%d) must end at complete older turns",
			summary.SourceMessages, summary.ReplacedMessages,
		)
	}
	prefix := make([]*model.Message, 0, systemEnd+1)
	prefix = append(prefix, messages[:systemEnd]...)
	prefix = append(prefix, &summary.Message)
	return requestShape(prefix, turns, keepStart), nil
}

// historyConversationCount measures the source and replacement prefixes in
// conversational messages. System instructions are never part of either prefix.
func historyConversationCount(turns []turn) int {
	count := 0
	for _, turn := range turns {
		for _, message := range turn.messages {
			if message.Role != model.ConversationRoleSystem {
				count++
			}
		}
	}
	return count
}

// systemPrefixEnd finds the insertion point after original leading instructions.
// Later instructions stay where requestShape places their original turns.
func systemPrefixEnd(messages []*model.Message) int {
	for i, message := range messages {
		if message.Role != model.ConversationRoleSystem {
			return i
		}
	}
	return len(messages)
}
