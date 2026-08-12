// Package tooluseid translates canonical transcript correlation IDs into IDs
// accepted by model-provider wire contracts.
package tooluseid

import (
	"fmt"

	"goa.design/goa-ai/runtime/agent/model"
)

const maxProviderIDLength = 64

// Mapper assigns stable, collision-free provider IDs within one encoded
// request. Canonical IDs remain unchanged in the transcript.
type Mapper struct {
	aliases map[string]string
	used    map[string]struct{}
	next    int
}

// NewMapper reserves every provider-safe tool-use ID in messages before
// aliases are assigned. Reserving first keeps the mapping bijective even when
// an invalid canonical ID appears before a safe ID such as "t1".
func NewMapper(messages []*model.Message) *Mapper {
	mapper := &Mapper{
		aliases: make(map[string]string),
		used:    make(map[string]struct{}),
	}
	for _, message := range messages {
		if message == nil {
			continue
		}
		for _, part := range message.Parts {
			use, ok := part.(model.ToolUsePart)
			if ok && isProviderSafe(use.ID) {
				mapper.used[use.ID] = struct{}{}
			}
		}
	}
	return mapper
}

// ID returns the request-local provider representation of canonical. Empty
// IDs remain empty so the provider adapter retains ownership of required-field
// validation.
func (m *Mapper) ID(canonical string) string {
	if canonical == "" || isProviderSafe(canonical) {
		return canonical
	}
	if id, ok := m.aliases[canonical]; ok {
		return id
	}
	for {
		m.next++
		id := fmt.Sprintf("t%d", m.next)
		if _, exists := m.used[id]; exists {
			continue
		}
		m.aliases[canonical] = id
		m.used[id] = struct{}{}
		return id
	}
}

// isProviderSafe reports whether id satisfies the shared Anthropic Messages
// and Bedrock Converse tool-use ID contract.
func isProviderSafe(id string) bool {
	if id == "" || len(id) > maxProviderIDLength {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_':
		case r == '-':
		default:
			return false
		}
	}
	return true
}
