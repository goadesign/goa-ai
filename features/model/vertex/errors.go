package vertex

import (
	"errors"
	"net/http"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

// wrapGeminiError classifies a Gemini/Vertex failure into the goa-ai
// provider error contract. Throttling joins model.ErrRateLimited so the
// adaptive rate-limit middleware backs off; adapters never retry.
//
// The genai SDK (v1.62.0) does not surface *googleapi.Error for HTTP
// failures from GenerateContent/GenerateContentStream/CountTokens; it
// defines its own value type, genai.APIError (see api_client.go's
// newAPIError), carrying the HTTP status in Code and the server message in
// Message. That is the type classified here.
func wrapGeminiError(operation string, err error) error {
	if err == nil {
		return nil
	}
	status := 0
	message := err.Error()
	var gerr genai.APIError
	if errors.As(err, &gerr) {
		status = gerr.Code
		message = gerr.Message
	}
	return classifyProviderError(geminiProviderName, operation, status, message, err)
}

// classifyProviderError maps an HTTP status code to the goa-ai provider
// error contract. It is shared by wrapGeminiError and
// wrapAnthropicVertexError (see anthropic.go) so the status-to-kind
// classification table is defined exactly once for every Vertex-hosted
// model adapter. Throttling (429) joins model.ErrRateLimited so the
// adaptive rate-limit middleware backs off; adapters never retry
// themselves.
func classifyProviderError(provider, operation string, status int, message string, cause error) error {
	kind := model.ProviderErrorKindUnknown
	retryable := false
	switch {
	case status == http.StatusTooManyRequests:
		pe := model.NewProviderError(provider, operation, status,
			model.ProviderErrorKindRateLimited, "rate_limited", message, "", true, cause)
		return errors.Join(model.ErrRateLimited, pe)
	case status == http.StatusBadRequest:
		kind = model.ProviderErrorKindInvalidRequest
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		kind = model.ProviderErrorKindAuth
	case status >= 500 && status < 600:
		// Widened from the narrower [500, 511] (StatusNetworkAuthenticationRequired)
		// range: any 5xx is a server-side failure and should be treated as
		// unavailable/retryable, not left unclassified.
		kind = model.ProviderErrorKindUnavailable
		retryable = true
	}
	return model.NewProviderError(provider, operation, status, kind, "", message, "", retryable, cause)
}
