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

// TestWrapGeminiError verifies genai.APIError extraction feeds
// model.ClassifyHTTPStatus correctly; the status-to-kind table itself is
// tested exhaustively in runtime/agent/model.
func TestWrapGeminiError(t *testing.T) {
	src := genai.APIError{Code: http.StatusTooManyRequests, Message: "boom"}
	err := wrapGeminiError("generate_content", src)
	require.ErrorIs(t, err, model.ErrRateLimited)
	pe, ok := model.AsProviderError(err)
	require.True(t, ok)
	assert.Equal(t, model.ProviderErrorKindRateLimited, pe.Kind())
	assert.Equal(t, geminiProviderName, pe.Provider())
	assert.Equal(t, "generate_content", pe.Operation())
	assert.Equal(t, "boom", pe.Message())
}

func TestWrapGeminiErrorNonAPI(t *testing.T) {
	err := wrapGeminiError("generate_content", errors.New("dial tcp: timeout"))
	pe, ok := model.AsProviderError(err)
	require.True(t, ok)
	assert.Equal(t, model.ProviderErrorKindUnknown, pe.Kind())
	assert.Equal(t, 0, pe.HTTPStatus())
}

func TestWrapGeminiErrorNil(t *testing.T) {
	assert.NoError(t, wrapGeminiError("generate_content", nil))
}
