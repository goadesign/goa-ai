package tooluseid

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestMapperID(t *testing.T) {
	t.Parallel()

	messages := []*model.Message{{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{
			model.ToolUsePart{ID: "run/turn/call"},
			model.ToolUsePart{ID: "t1"},
		},
	}}
	mapper := NewMapper(messages)

	assert.Equal(t, "t2", mapper.ID("run/turn/call"))
	assert.Equal(t, "t2", mapper.ID("run/turn/call"))
	assert.Equal(t, "t1", mapper.ID("t1"))
}

func TestMapperIDProviderContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		canonical string
		want      string
	}{
		{name: "empty", canonical: "", want: ""},
		{name: "letters digits underscore and hyphen", canonical: "tool_use-123", want: "tool_use-123"},
		{name: "slash", canonical: "run/turn/call", want: "t1"},
		{name: "unicode", canonical: "töölu", want: "t1"},
		{name: "too long", canonical: strings.Repeat("a", maxProviderIDLength+1), want: "t1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, NewMapper(nil).ID(test.canonical))
		})
	}
}
