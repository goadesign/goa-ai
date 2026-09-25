package model

// These tests check that explicit local request rejections retain their original
// errors instead of inventing provider evidence or replacing diagnostic text.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestValidationErrorPreservesCause(t *testing.T) {
	cause := errors.New(strings.Repeat("request diagnostic\né", 1000))
	rejection := NewRequestValidationError(cause)
	require.EqualError(t, rejection, cause.Error())
	assert.Same(t, cause, rejection.Unwrap())
	wrapped := fmt.Errorf("prepare request: %w", rejection)
	require.ErrorIs(t, wrapped, cause)
	var got *RequestValidationError
	require.ErrorAs(t, wrapped, &got)
	assert.Same(t, rejection, got)
	_, provider := AsProviderError(wrapped)
	assert.False(t, provider)
}

func TestRequestValidationErrorRequiresCause(t *testing.T) {
	assert.PanicsWithValue(t, "model: request validation error requires a cause", func() {
		assert.NotNil(t, NewRequestValidationError(nil))
	})
}

func TestRequestByteCapacityRetainsTerminalIdentity(t *testing.T) {
	for _, err := range []error{
		ErrRequestByteCapacity,
		fmt.Errorf("encoded request: %w", ErrRequestByteCapacity),
		NewRequestValidationError(fmt.Errorf("adapter rejected request: %w", ErrRequestByteCapacity)),
	} {
		require.ErrorIs(t, err, ErrRequestByteCapacity)
		var rejected *RequestValidationError
		require.ErrorAs(t, err, &rejected)
		require.NotErrorIs(t, err, ErrImageSourceCapacity)
		_, provider := AsProviderError(err)
		assert.False(t, provider)
	}
	// Matching diagnostic text does not establish a locally measured byte limit.
	unmarked := NewRequestValidationError(errors.New(ErrRequestByteCapacity.Error()))
	require.NotErrorIs(t, unmarked, ErrRequestByteCapacity)
}
