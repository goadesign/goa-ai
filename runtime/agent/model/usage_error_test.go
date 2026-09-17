// Failed multi-request invocations must retain counts without changing retry
// classification or allowing callers to mutate retained evidence.
package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetainUsagePreservesFailureAndOwnsCounts(t *testing.T) {
	for _, cause := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		NewProviderError("test", "create", 429, ProviderErrorKindRateLimited, "", "", "", true, ErrRateLimited),
	} {
		usage := TokenUsage{InputTokens: 8, OutputTokens: 3, TotalTokens: 11}
		err := RetainUsage(cause, usage)
		require.ErrorIs(t, err, cause)
		assert.Equal(t, &usage, UsageFromError(err))
		UsageFromError(err).InputTokens = 100
		assert.Equal(t, 8, UsageFromError(err).InputTokens)
		var rejected *OutputValidationError
		assert.NotErrorAs(t, err, &rejected)
		if provider, ok := AsProviderError(cause); ok {
			actual, found := AsProviderError(err)
			assert.True(t, found)
			assert.Same(t, provider, actual)
		}
	}
	err := RetainUsage(context.Canceled, TokenUsage{InputTokens: -1})
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, UsageFromError(err))
}

func TestRetainUsageKeepsExactOutputRejection(t *testing.T) {
	originalUsage := TokenUsage{InputTokens: 3}
	total := TokenUsage{InputTokens: 10, OutputTokens: 2}
	cause := errors.New("malformed native output")
	rejected, err := RestoreOutputValidationError(OutputValidationResponseShape, cause, ResponseEvidence{}, &originalUsage)
	require.NoError(t, err)
	retained := RetainUsage(rejected, total)
	exact, ok := retained.(*OutputValidationError) //nolint:errorlint // The transport requires the exact root category.
	require.True(t, ok)
	assert.Equal(t, OutputValidationResponseShape, exact.Kind())
	assert.Equal(t, &total, exact.Usage())
	assert.Equal(t, &originalUsage, rejected.Usage())
	require.ErrorIs(t, retained, rejected)
	require.ErrorIs(t, retained, cause)
	assert.Equal(t, &total, UsageFromError(fmt.Errorf("transport: %w", retained)))

	mixed := RetainUsage(errors.Join(rejected, context.Canceled), total)
	_, exactRoot := mixed.(*OutputValidationError) //nolint:errorlint // Mixed operation failures must not become exact output rejections.
	assert.False(t, exactRoot)
	require.ErrorIs(t, mixed, context.Canceled)
	assert.Equal(t, &total, UsageFromError(mixed))
	updated := RetainUsage(exact, TokenUsage{InputTokens: 20})
	assert.Equal(t, 20, UsageFromError(updated).InputTokens)
}

func TestAddTokenUsageRejectsOverflowAndInvalidCounts(t *testing.T) {
	sum, err := AddTokenUsage(
		TokenUsage{Model: "first", InputTokens: 8, OutputTokens: 2, TotalTokens: 10},
		TokenUsage{Model: "second", InputTokens: 3, OutputTokens: 4, TotalTokens: 7},
	)
	require.NoError(t, err)
	assert.Equal(t, TokenUsage{InputTokens: 11, OutputTokens: 6, TotalTokens: 17}, sum)
	_, err = AddTokenUsage(TokenUsage{OutputTokens: math.MaxInt}, TokenUsage{OutputTokens: 1})
	require.ErrorContains(t, err, "integer range")
	_, err = AddTokenUsage(TokenUsage{}, TokenUsage{TotalTokens: -1})
	require.ErrorContains(t, err, "negative")
}
