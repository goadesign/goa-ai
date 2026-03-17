package naming

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeToken(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		fallback string
		want     string
	}{
		{name: "snake case punctuation", input: "Remote-Tools.v1", fallback: "toolset", want: "remote_tools_v1"},
		{name: "collapses empty to fallback", input: "!!!", fallback: "toolset", want: "toolset"},
		{name: "trims repeated separators", input: "__remote__tools__", fallback: "toolset", want: "remote_tools"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.want, SanitizeToken(testCase.input, testCase.fallback))
		})
	}
}

func TestQueueName(t *testing.T) {
	assert.Equal(t, "service_agent_toolset", QueueName("Service", "Agent", "toolset"))
}

func TestIdentifier(t *testing.T) {
	assert.Equal(t, "service.agent.toolset", Identifier("Service", "Agent", "toolset"))
}
