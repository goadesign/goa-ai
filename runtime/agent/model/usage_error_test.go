// Failed multi-request invocations must retain counts without changing retry
// classification or allowing callers to mutate retained evidence.
package model

import (
	"context"
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
