package temporaltrace

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

func TestActivityTracingUsesGenericSpanErrorContract(t *testing.T) {
	t.Parallel()

	t.Run("done context suppresses cancellation", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		assert.False(t, telemetry.ShouldRecordSpanError(ctx, context.Canceled))
	})

	t.Run("live context records cancellation", func(t *testing.T) {
		t.Parallel()

		assert.True(t, telemetry.ShouldRecordSpanError(context.Background(), context.Canceled))
	})
}
