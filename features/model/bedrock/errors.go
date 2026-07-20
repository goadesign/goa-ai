// Package bedrock translates AWS Bedrock failures into the provider-neutral
// model error contract. This file owns provider error classification and the
// exact boundary codecs for successful outcomes AWS exposes as errors.
package bedrock

import (
	"errors"
	"fmt"
	"net/http"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"goa.design/goa-ai/runtime/agent/model"
)

// isRateLimited reports whether err represents a provider rate limiting
// condition. It treats both HTTP 429 responses and provider error codes like
// ThrottlingException as rate-limited signals and is idempotent when
// ErrRateLimited is already present in the error chain.
func isRateLimited(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, model.ErrRateLimited) {
		return true
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "ThrottlingException", "TooManyRequestsException":
			return true
		}
	}
	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == 429 {
		return true
	}

	return false
}

// wrapBedrockError preserves provider identity, operation, status, code, and
// retry semantics while retaining the complete AWS error chain.
func wrapBedrockError(operation string, err error) error {
	var (
		status    int
		code      string
		msg       string
		requestID string
	)

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code = apiErr.ErrorCode()
		msg = apiErr.ErrorMessage()
	}
	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) {
		status = respErr.HTTPStatusCode()
	}
	var awsRespErr *awshttp.ResponseError
	if errors.As(err, &awsRespErr) {
		requestID = awsRespErr.ServiceRequestID()
	}

	if isRateLimited(err) {
		if status == 0 {
			status = http.StatusTooManyRequests
		}
		if code == "" {
			code = "rate_limited"
		}
		pe := model.NewProviderError(bedrockProviderName, operation, status, model.ProviderErrorKindRateLimited, code, msg, requestID, true, err)
		return errors.Join(model.ErrRateLimited, pe)
	}

	kind := model.ProviderErrorKindUnknown
	retryable := false
	switch {
	case status == http.StatusBadRequest:
		kind = model.ProviderErrorKindInvalidRequest
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		kind = model.ProviderErrorKindAuth
	case status == http.StatusTooManyRequests:
		kind = model.ProviderErrorKindRateLimited
		retryable = true
	case status >= http.StatusInternalServerError && status <= http.StatusNetworkAuthenticationRequired:
		kind = model.ProviderErrorKindUnavailable
		retryable = true
	}

	return model.NewProviderError(bedrockProviderName, operation, status, kind, code, msg, requestID, retryable, err)
}

// promptTooLongTokenCount decodes the exact input count Bedrock reports when
// CountTokens measures a valid request beyond the model context window. AWS
// exposes this outcome only as a ValidationException message rather than a
// typed count result. Match the complete provider message and its invariant
// (input > maximum) so every other validation failure remains an error.
func promptTooLongTokenCount(err error) (int, bool) {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "ValidationException" {
		return 0, false
	}
	message := apiErr.ErrorMessage()
	var inputTokens, maximumTokens int
	n, scanErr := fmt.Sscanf(
		message,
		"prompt is too long: %d tokens > %d maximum",
		&inputTokens,
		&maximumTokens,
	)
	if scanErr != nil || n != 2 || inputTokens <= maximumTokens || maximumTokens <= 0 {
		return 0, false
	}
	if message != fmt.Sprintf(
		"prompt is too long: %d tokens > %d maximum",
		inputTokens,
		maximumTokens,
	) {
		return 0, false
	}
	return inputTokens, true
}
