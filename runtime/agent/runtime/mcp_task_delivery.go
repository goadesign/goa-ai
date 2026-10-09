// Package runtime separates Task transport failures from observed terminal results.
// Workflow timers own repeated reads and cancellation delivery. Neither repeats the creating tool or uncertain answer submission.
package runtime

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/mcp"
)

// mcpTaskDeliveryError permits retries only for a read or cancellation with a
// temporary transport failure. A sent answer may already have been applied, so
// its lost acknowledgment must stop rather than send the answers again.
func mcpTaskDeliveryError(operation *api.ExecutionContinuation, err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	_, reading := operation.AsTaskGet()
	_, cancelling := operation.AsTaskCancel()
	if (reading || cancelling) && temporaryMCPTaskDelivery(err) {
		return err
	}
	return engine.MarkActivityErrorNonRetryable(err)
}

// temporaryMCPTaskDelivery recognizes failed HTTP delivery and connection loss.
// Protocol rejection, malformed output, local bugs and cancellation are final.
// A rate limit or unavailable endpoint does not discard an accepted Task.
func temporaryMCPTaskDelivery(err error) bool {
	var malformed *mcp.MalformedResponseError
	var internal *mcp.InternalError
	var protocol *mcp.Error
	if errors.Is(err, context.Canceled) || errors.As(err, &protocol) {
		return false
	}
	var response *mcp.HTTPResponseError
	if errors.As(err, &response) {
		switch response.StatusCode {
		case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	if errors.As(err, &malformed) || errors.As(err, &internal) {
		return false
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE)
}
