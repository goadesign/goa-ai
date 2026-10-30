// Package gateway encodes and decodes structured model, provider, validation,
// and sentinel errors across the Goa-generated gRPC wire contract.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"

	genmodelgateway "goa.design/goa-ai/features/model/gateway/gen/model_gateway"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

// Error node kind and sentinel discriminators carried inside GatewayErrorNode.
const (
	errorKindPlain        = "plain"
	errorKindSentinel     = "sentinel"
	errorKindProvider     = "provider"
	errorKindValidation   = "validation"
	errorKindUnadvertised = "unadvertised_tool"
	errorKindMalformed    = "malformed_tool_args"
	errorKindJoined       = "joined"

	sentinelStreamingUnsupported        = "streaming_unsupported"
	sentinelStructuredOutputUnsupported = "structured_output_unsupported"
	sentinelToolSearchUnsupported       = "tool_search_unsupported"
	sentinelToolContentUnsupported      = "tool_content_unsupported"
	sentinelRateLimited                 = "rate_limited"
	sentinelEmptyStream                 = "empty_stream"
	sentinelTokenCountingUnsupported    = "token_counting_unsupported"
	sentinelContextCanceled             = "context_canceled"
	sentinelContextDeadlineExceeded     = "context_deadline_exceeded"
)

// toGenError encodes an error tree into a Goa-generated GatewayError while
// preserving OutputValidationError, ProviderError, sentinel errors, and
// retained usage.
func toGenError(err error) *genmodelgateway.GatewayError {
	if err == nil {
		return nil
	}
	var nodes []*genmodelgateway.GatewayErrorNode
	encodeErrorNode(err, &nodes)
	out := &genmodelgateway.GatewayError{
		Nodes: nodes,
	}
	// Attach top-level retained usage when the root node is not already a
	// direct OutputValidationError (which carries its own Usage field).
	if len(nodes) > 0 && nodes[0].Kind != errorKindValidation {
		if usage := model.UsageFromError(err); usage != nil {
			out.RetainedUsage = toGenUsage(*usage)
		}
	}
	return out
}

// encodeErrorNode appends one error node and its descendants to nodes and
// returns the appended node's index (-1 when err is nil).
func encodeErrorNode(err error, nodes *[]*genmodelgateway.GatewayErrorNode) int {
	if err == nil {
		return -1
	}
	//nolint:errorlint // Inspect exact node before unwrapping children.
	if validationErr, ok := err.(*model.OutputValidationError); ok && validationErr != nil {
		ev := validationErr.Evidence()
		detail := &genmodelgateway.ValidationErrorDetail{
			Kind: string(validationErr.Kind()),
			Evidence: &genmodelgateway.ResponseEvidence{
				Present: ev.Present,
				Version: ev.Version,
				Sha256:  ev.SHA256,
				Size:    ev.Size,
			},
			RecoveryCorrection: validationErr.RecoveryCorrection(),
		}
		if u := validationErr.Usage(); u != nil {
			detail.Usage = toGenUsage(*u)
		}
		idx := len(*nodes)
		node := &genmodelgateway.GatewayErrorNode{
			Kind:       errorKindValidation,
			Validation: detail,
			CauseIndex: -1,
		}
		*nodes = append(*nodes, node)
		node.CauseIndex = encodeErrorNode(errors.Unwrap(validationErr), nodes)
		return idx
	}
	//nolint:errorlint // Inspect exact node before unwrapping children.
	if providerErr, ok := err.(*model.ProviderError); ok && providerErr != nil {
		idx := len(*nodes)
		node := &genmodelgateway.GatewayErrorNode{
			Kind: errorKindProvider,
			Provider: &genmodelgateway.ProviderErrorDetail{
				Provider:   providerErr.Provider(),
				Operation:  providerErr.Operation(),
				HTTPStatus: providerErr.HTTPStatus(),
				Kind:       string(providerErr.Kind()),
				Code:       providerErr.Code(),
				Message:    providerErr.Message(),
				RequestID:  providerErr.RequestID(),
				Retryable:  providerErr.Retryable(),
			},
			CauseIndex: -1,
		}
		*nodes = append(*nodes, node)
		node.CauseIndex = encodeErrorNode(errors.Unwrap(providerErr), nodes)
		return idx
	}
	if sentinel, ok := sentinelCode(err); ok {
		idx := len(*nodes)
		*nodes = append(*nodes, &genmodelgateway.GatewayErrorNode{
			Kind:       errorKindSentinel,
			Sentinel:   sentinel,
			CauseIndex: -1,
		})
		return idx
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		idx := len(*nodes)
		node := &genmodelgateway.GatewayErrorNode{
			Kind:       errorKindJoined,
			CauseIndex: -1,
		}
		*nodes = append(*nodes, node)
		for _, child := range joined.Unwrap() {
			if child != nil {
				if childIdx := encodeErrorNode(child, nodes); childIdx >= 0 {
					node.ChildIndices = append(node.ChildIndices, childIdx)
				}
			}
		}
		return idx
	}
	if name, ok := model.UnadvertisedToolName(err); ok && errors.Unwrap(err) == nil {
		idx := len(*nodes)
		*nodes = append(*nodes, &genmodelgateway.GatewayErrorNode{
			Kind:                 errorKindUnadvertised,
			UnadvertisedToolName: name,
			CauseIndex:           -1,
		})
		return idx
	}
	//nolint:errorlint // Inspect exact node identity before recursing into cause.
	if toolName, cause, ok := model.MalformedToolArgumentsToolName(err); ok && errors.Unwrap(err) == cause {
		idx := len(*nodes)
		node := &genmodelgateway.GatewayErrorNode{
			Kind:              errorKindMalformed,
			MalformedToolName: string(toolName),
			CauseIndex:        -1,
		}
		*nodes = append(*nodes, node)
		node.CauseIndex = encodeErrorNode(cause, nodes)
		return idx
	}
	if inner := errors.Unwrap(err); inner != nil {
		// Unwrap usageError transparently because top-level RetainedUsage is
		// captured on the root GatewayError.
		if inner.Error() == err.Error() {
			return encodeErrorNode(inner, nodes)
		}
	}
	idx := len(*nodes)
	node := &genmodelgateway.GatewayErrorNode{
		Kind:       errorKindPlain,
		Message:    err.Error(),
		CauseIndex: -1,
	}
	*nodes = append(*nodes, node)
	node.CauseIndex = encodeErrorNode(errors.Unwrap(err), nodes)
	return idx
}

// fromGenError reconstructs a Go error from a Goa-generated GatewayError.
func fromGenError(in *genmodelgateway.GatewayError) error {
	if in == nil || len(in.Nodes) == 0 {
		return nil
	}
	err := decodeErrorNode(in.Nodes, 0)
	if err != nil && in.RetainedUsage != nil && in.Nodes[0] != nil && in.Nodes[0].Kind != errorKindValidation {
		err = model.RetainUsage(err, fromGenUsage(in.RetainedUsage))
	}
	return err
}

// decodeErrorNode recursively reconstructs one error node from nodes[idx].
func decodeErrorNode(nodes []*genmodelgateway.GatewayErrorNode, idx int) error {
	if idx < 0 || idx >= len(nodes) {
		return nil
	}
	w := nodes[idx]
	if w == nil {
		return nil
	}
	decodeChild := func(childIdx int) error {
		if childIdx <= idx || childIdx >= len(nodes) {
			return nil
		}
		return decodeErrorNode(nodes, childIdx)
	}
	switch w.Kind {
	case errorKindSentinel:
		if err := fromSentinelCode(w.Sentinel); err != nil {
			return err
		}
		return errors.New(w.Message)
	case errorKindProvider:
		if w.Provider == nil {
			return errors.New("gateway: malformed provider error")
		}
		provider := w.Provider.Provider
		if provider == "" {
			provider = "gateway"
		}
		kind := model.ProviderErrorKind(w.Provider.Kind)
		if kind == "" {
			kind = model.ProviderErrorKindUnknown
		}
		return model.NewProviderError(
			provider,
			w.Provider.Operation,
			w.Provider.HTTPStatus,
			kind,
			w.Provider.Code,
			w.Provider.Message,
			w.Provider.RequestID,
			w.Provider.Retryable,
			decodeChild(w.CauseIndex),
		)
	case errorKindValidation:
		if w.Validation == nil {
			return errors.New("gateway: malformed output validation error")
		}
		cause := decodeChild(w.CauseIndex)
		if cause == nil {
			cause = errors.New("model output rejected by gateway")
		}
		var ev model.ResponseEvidence
		if w.Validation.Evidence != nil {
			ev = model.ResponseEvidence{
				Present: w.Validation.Evidence.Present,
				Version: w.Validation.Evidence.Version,
				SHA256:  w.Validation.Evidence.Sha256,
				Size:    w.Validation.Evidence.Size,
			}
		}
		var usage *model.TokenUsage
		if w.Validation.Usage != nil {
			u := fromGenUsage(w.Validation.Usage)
			usage = &u
		}
		restored, err := model.RestoreOutputValidationError(
			model.OutputValidationKind(w.Validation.Kind),
			cause,
			ev,
			usage,
		)
		if err != nil {
			return err
		}
		if w.Validation.RecoveryCorrection != "" {
			correctable, err := model.RestoreCorrectableOutputValidationError(
				restored,
				w.Validation.RecoveryCorrection,
			)
			if err != nil {
				return err
			}
			return correctable
		}
		return restored
	case errorKindUnadvertised:
		if w.UnadvertisedToolName != "" {
			return model.NewUnadvertisedToolNameError(w.UnadvertisedToolName)
		}
		return errors.New("model returned an unadvertised tool name")
	case errorKindMalformed:
		cause := decodeChild(w.CauseIndex)
		if cause == nil {
			cause = errors.New("invalid JSON")
		}
		if w.MalformedToolName != "" {
			return model.NewMalformedToolArgumentsError(tools.Ident(w.MalformedToolName), cause)
		}
		return cause
	case errorKindJoined:
		children := make([]error, 0, len(w.ChildIndices))
		for _, childIdx := range w.ChildIndices {
			if decoded := decodeChild(childIdx); decoded != nil {
				children = append(children, decoded)
			}
		}
		return errors.Join(children...)
	default:
		cause := decodeChild(w.CauseIndex)
		if cause == nil {
			return errors.New(w.Message)
		}
		if w.Message == "" || w.Message == cause.Error() {
			return cause
		}
		prefix := strings.TrimSuffix(w.Message, ": "+cause.Error())
		return fmt.Errorf("%s: %w", prefix, cause)
	}
}

// sentinelCode returns the wire code for an exact sentinel error value.
func sentinelCode(err error) (string, bool) {
	//nolint:errorlint // Check exact node identity so wrapped chains preserve outer context.
	switch err {
	case model.ErrStreamingUnsupported:
		return sentinelStreamingUnsupported, true
	case model.ErrStructuredOutputUnsupported:
		return sentinelStructuredOutputUnsupported, true
	case model.ErrToolSearchUnsupported:
		return sentinelToolSearchUnsupported, true
	case model.ErrToolContentUnsupported:
		return sentinelToolContentUnsupported, true
	case model.ErrRateLimited:
		return sentinelRateLimited, true
	case model.ErrEmptyStream:
		return sentinelEmptyStream, true
	case model.ErrTokenCountingUnsupported:
		return sentinelTokenCountingUnsupported, true
	case context.Canceled:
		return sentinelContextCanceled, true
	case context.DeadlineExceeded:
		return sentinelContextDeadlineExceeded, true
	default:
		return "", false
	}
}

// fromSentinelCode maps a wire sentinel code back to the exact sentinel error.
func fromSentinelCode(code string) error {
	switch code {
	case sentinelStreamingUnsupported:
		return model.ErrStreamingUnsupported
	case sentinelStructuredOutputUnsupported:
		return model.ErrStructuredOutputUnsupported
	case sentinelToolSearchUnsupported:
		return model.ErrToolSearchUnsupported
	case sentinelToolContentUnsupported:
		return model.ErrToolContentUnsupported
	case sentinelRateLimited:
		return model.ErrRateLimited
	case sentinelEmptyStream:
		return model.ErrEmptyStream
	case sentinelTokenCountingUnsupported:
		return model.ErrTokenCountingUnsupported
	case sentinelContextCanceled:
		return context.Canceled
	case sentinelContextDeadlineExceeded:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
