package vertex

import (
	"errors"

	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

// wrapGeminiError classifies a Gemini/Vertex failure into the goa-ai
// provider error contract via model.ClassifyHTTPStatus. Throttling joins
// model.ErrRateLimited so the adaptive rate-limit middleware backs off;
// adapters never retry.
//
// The genai SDK (v1.62.0) does not surface *googleapi.Error for HTTP
// failures from GenerateContent/GenerateContentStream/CountTokens; it
// defines its own value type, genai.APIError (see api_client.go's
// newAPIError), carrying the HTTP status in Code and the server message in
// Message. That is the type extracted here.
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
	return model.ClassifyHTTPStatus(geminiProviderName, operation, status, message, err)
}
