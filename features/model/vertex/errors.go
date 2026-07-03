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
	kind := model.ProviderErrorKindUnknown
	retryable := false
	switch {
	case status == http.StatusTooManyRequests:
		pe := model.NewProviderError(geminiProviderName, operation, status,
			model.ProviderErrorKindRateLimited, "rate_limited", message, "", true, err)
		return errors.Join(model.ErrRateLimited, pe)
	case status == http.StatusBadRequest:
		kind = model.ProviderErrorKindInvalidRequest
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		kind = model.ProviderErrorKindAuth
	case status >= http.StatusInternalServerError && status <= http.StatusNetworkAuthenticationRequired:
		kind = model.ProviderErrorKindUnavailable
		retryable = true
	}
	return model.NewProviderError(geminiProviderName, operation, status, kind, "", message, "", retryable, err)
}
