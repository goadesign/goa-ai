package openai

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeToolName(t *testing.T) {
	t.Run("replaces dots and invalid characters", func(t *testing.T) {
		got := SanitizeToolName("analytics.analyze/v2")
		assert.Equal(t, "analytics_analyze_v2", got)
	})

	t.Run("truncates long names with stable suffix", func(t *testing.T) {
		input := strings.Repeat("segment.", 20)
		got := SanitizeToolName(input)
		assert.Len(t, got, 64)
		assert.Contains(t, got, "_")
	})
}
