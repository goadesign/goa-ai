// Package bedrock translates AWS Bedrock failures into the provider-neutral
// model error contract. This file owns provider error classification and the
// exact boundary codecs for successful outcomes AWS exposes as errors.
package bedrock

import (
	"context"
	"errors"
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
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
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

	kind, retryable, codeStatus := classifyBedrockErrorCode(code)
	if status == 0 {
		status = codeStatus
	}
	if kind == model.ProviderErrorKindUnknown {
		switch {
		case status == http.StatusBadRequest:
			kind = model.ProviderErrorKindInvalidRequest
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			kind = model.ProviderErrorKindAuth
		case status == http.StatusTooManyRequests:
			kind = model.ProviderErrorKindRateLimited
			retryable = true
		case status >= http.StatusInternalServerError && status < 600:
			kind = model.ProviderErrorKindUnavailable
			retryable = true
		}
	}

	return model.NewProviderError(bedrockProviderName, operation, status, kind, code, msg, requestID, retryable, err)
}

// classifyBedrockErrorCode handles event-stream exceptions that arrive after
// the HTTP request succeeded and therefore carry no response status. The
// returned status is the value documented for that Bedrock exception.
func classifyBedrockErrorCode(code string) (model.ProviderErrorKind, bool, int) {
	switch code {
	case "ValidationException":
		return model.ProviderErrorKindInvalidRequest, false, http.StatusBadRequest
	case "AccessDeniedException":
		return model.ProviderErrorKindAuth, false, http.StatusForbidden
	case "InternalServerException":
		return model.ProviderErrorKindUnavailable, true, http.StatusInternalServerError
	case "ServiceUnavailableException":
		return model.ProviderErrorKindUnavailable, true, http.StatusServiceUnavailable
	case "ModelTimeoutException":
		return model.ProviderErrorKindUnavailable, true, http.StatusRequestTimeout
	case "ModelStreamErrorException":
		return model.ProviderErrorKindUnavailable, true, 424
	default:
		return model.ProviderErrorKindUnknown, false, 0
	}
}
