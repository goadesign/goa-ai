package toolregistry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToolUseIDContextRoundTrip(t *testing.T) {
	t.Parallel()

	const toolUseID = "use-1"
	ctx := WithToolUseID(context.Background(), toolUseID)
	got, ok := ToolUseIDFromContext(ctx)
	assert.True(t, ok)
	assert.Equal(t, toolUseID, got)
}
