package model

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyHTTPStatus(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		kind      ProviderErrorKind
		retryable bool
		rateLtd   bool
	}{
		{"429", http.StatusTooManyRequests, ProviderErrorKindRateLimited, true, true},
		{"400", http.StatusBadRequest, ProviderErrorKindInvalidRequest, false, false},
		{"401", http.StatusUnauthorized, ProviderErrorKindAuth, false, false},
		{"403", http.StatusForbidden, ProviderErrorKindAuth, false, false},
		{"503", http.StatusServiceUnavailable, ProviderErrorKindUnavailable, true, false},
		// 520 is outside the narrower bound some providers historically used
		// (500-511, i.e. StatusNetworkAuthenticationRequired) but is still a
		// server-side 5xx failure (a Cloudflare-style "unknown error" code
		// some upstreams surface) and must classify as unavailable/retryable.
		{"520", 520, ProviderErrorKindUnavailable, true, false},
		// 599/600 pin the upper bound of the 5xx band: 599 is the last
		// status classified as unavailable/retryable; 600 falls outside it.
		{"599", 599, ProviderErrorKindUnavailable, true, false},
		{"600", 600, ProviderErrorKindUnknown, false, false},
		{"unknown", 418, ProviderErrorKindUnknown, false, false},
		{"zero", 0, ProviderErrorKindUnknown, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cause := errors.New("boom")
			err := ClassifyHTTPStatus("test-provider", "generate_content", tc.status, "boom", cause)
			assert.Equal(t, tc.rateLtd, errors.Is(err, ErrRateLimited))
			pe, ok := AsProviderError(err)
			require.True(t, ok)
			assert.Equal(t, tc.kind, pe.Kind())
			assert.Equal(t, tc.retryable, pe.Retryable())
			assert.Equal(t, tc.status, pe.HTTPStatus())
			assert.Equal(t, "test-provider", pe.Provider())
			assert.Equal(t, "generate_content", pe.Operation())
			assert.ErrorIs(t, err, cause)
		})
	}
}

func TestClassifyHTTPStatusPreservesRateLimitedCause(t *testing.T) {
	// A pre-classified sentinel (status 0) must still satisfy errors.Is via
	// the Unwrap chain even though the status alone does not select the
	// rate_limited kind.
	err := ClassifyHTTPStatus("test-provider", "complete", 0, "boom", ErrRateLimited)
	require.ErrorIs(t, err, ErrRateLimited)
	pe, ok := AsProviderError(err)
	require.True(t, ok)
	assert.Equal(t, ProviderErrorKindUnknown, pe.Kind())
}
