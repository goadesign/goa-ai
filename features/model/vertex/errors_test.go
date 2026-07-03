package vertex

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestWrapGeminiError(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		kind      model.ProviderErrorKind
		retryable bool
		rateLtd   bool
	}{
		{"429", http.StatusTooManyRequests, model.ProviderErrorKindRateLimited, true, true},
		{"400", http.StatusBadRequest, model.ProviderErrorKindInvalidRequest, false, false},
		{"401", http.StatusUnauthorized, model.ProviderErrorKindAuth, false, false},
		{"403", http.StatusForbidden, model.ProviderErrorKindAuth, false, false},
		{"503", http.StatusServiceUnavailable, model.ProviderErrorKindUnavailable, true, false},
		// 520 is outside the old narrow bound (500-511, i.e.
		// StatusNetworkAuthenticationRequired) but is still a server-side
		// 5xx failure (a Cloudflare-style "unknown error" code some
		// upstreams surface) and must classify as unavailable/retryable.
		{"520", 520, model.ProviderErrorKindUnavailable, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := genai.APIError{Code: tc.status, Message: "boom"}
			err := wrapGeminiError("generate_content", src)
			assert.Equal(t, tc.rateLtd, errors.Is(err, model.ErrRateLimited))
			pe, ok := model.AsProviderError(err)
			require.True(t, ok)
			assert.Equal(t, tc.kind, pe.Kind())
		})
	}
}

func TestWrapGeminiErrorNonAPI(t *testing.T) {
	err := wrapGeminiError("generate_content", errors.New("dial tcp: timeout"))
	pe, ok := model.AsProviderError(err)
	require.True(t, ok)
	assert.Equal(t, model.ProviderErrorKindUnknown, pe.Kind())
}
