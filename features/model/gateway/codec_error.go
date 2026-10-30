// Package gateway encodes and decodes structured model, provider, validation,
// and sentinel errors across the Goa-generated gRPC wire contract.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	genmodelgateway "goa.design/goa-ai/features/model/gateway/gen/model_gateway"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// encodedErrorNode tracks an already-encoded comparable error value and its
	// index in GatewayError.Nodes so DAGs (such as errors.Join of identical
	// *model.OutputValidationError pointers) preserve shared node identity.
	encodedErrorNode struct {
		// err is the comparable error value.
		err error
		// idx is the index in GatewayError.Nodes.
		idx int64
	}
)

// Error node kind and sentinel discriminators carried inside GatewayErrorNode.
const (
	errorKindPlain             = "plain"
	errorKindSentinel          = "sentinel"
	errorKindProvider          = "provider"
	errorKindValidation        = "validation"
	errorKindRequestValidation = "request_validation"
	errorKindUnadvertised      = "unadvertised_tool"
	errorKindMalformed         = "malformed_tool_args"
	errorKindJoined            = "joined"

	sentinelStreamingUnsupported        = "streaming_unsupported"
	sentinelStructuredOutputUnsupported = "structured_output_unsupported"
	sentinelToolSearchUnsupported       = "tool_search_unsupported"
	sentinelToolContentUnsupported      = "tool_content_unsupported"
	sentinelRateLimited                 = "rate_limited"
	sentinelEmptyStream                 = "empty_stream"
	sentinelTokenCountingUnsupported    = "token_counting_unsupported"
	sentinelRequestByteCapacity         = "request_byte_capacity"
	sentinelImageSourceCapacity         = "image_source_capacity"
	sentinelContextCanceled             = "context_canceled"
	sentinelContextDeadlineExceeded     = "context_deadline_exceeded"
)

// toGenError encodes an error tree into a Goa-generated GatewayError while
// preserving OutputValidationError, RequestValidationError, ProviderError,
// sentinel errors, shared error node identity, and retained usage.
func toGenError(err error) *genmodelgateway.GatewayError {
	if err == nil {
		return nil
	}
	var nodes []*genmodelgateway.GatewayErrorNode
	var seen []encodedErrorNode
	encodeErrorNode(err, &nodes, &seen)
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
func encodeErrorNode(err error, nodes *[]*genmodelgateway.GatewayErrorNode, seen *[]encodedErrorNode) int64 {
	if err == nil {
		return -1
	}
	// Unwrap usageError transparently before checking node identity because
	// top-level RetainedUsage is captured on the root GatewayError.
	if _, isSentinel := sentinelCode(err); !isSentinel {
		//nolint:errorlint // Check exact root types before unwrapping transparent usage wrappers.
		_, isValidation := err.(*model.OutputValidationError)
		//nolint:errorlint // Check exact root types before unwrapping transparent usage wrappers.
		_, isReqValidation := err.(*model.RequestValidationError)
		//nolint:errorlint // Check exact root types before unwrapping transparent usage wrappers.
		_, isProvider := err.(*model.ProviderError)
		if !isValidation && !isReqValidation && !isProvider {
			if inner := errors.Unwrap(err); inner != nil && inner.Error() == err.Error() {
				_, _, isMalformedTool := model.MalformedToolArgumentsToolName(err)
				if !isMalformedTool {
					return encodeErrorNode(inner, nodes, seen)
				}
			}
		}
	}
	if idx, ok := findEncodedErrorNode(err, *seen); ok {
		return idx
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
		idx := int64(len(*nodes))
		node := &genmodelgateway.GatewayErrorNode{
			Kind:       errorKindValidation,
			Validation: detail,
			CauseIndex: -1,
		}
		*nodes = append(*nodes, node)
		rememberEncodedErrorNode(err, idx, seen)
		node.CauseIndex = encodeErrorNode(errors.Unwrap(validationErr), nodes, seen)
		return idx
	}
	if sentinel, ok := sentinelCode(err); ok {
		idx := int64(len(*nodes))
		*nodes = append(*nodes, &genmodelgateway.GatewayErrorNode{
			Kind:       errorKindSentinel,
			Sentinel:   sentinel,
			CauseIndex: -1,
		})
		rememberEncodedErrorNode(err, idx, seen)
		return idx
	}
	//nolint:errorlint // Inspect exact node before unwrapping children.
	if reqValidationErr, ok := err.(*model.RequestValidationError); ok && reqValidationErr != nil {
		idx := int64(len(*nodes))
		node := &genmodelgateway.GatewayErrorNode{
			Kind:       errorKindRequestValidation,
			Message:    reqValidationErr.Error(),
			CauseIndex: -1,
		}
		*nodes = append(*nodes, node)
		rememberEncodedErrorNode(err, idx, seen)
		node.CauseIndex = encodeErrorNode(errors.Unwrap(reqValidationErr), nodes, seen)
		return idx
	}
	//nolint:errorlint // Inspect exact node before unwrapping children.
	if providerErr, ok := err.(*model.ProviderError); ok && providerErr != nil {
		idx := int64(len(*nodes))
		node := &genmodelgateway.GatewayErrorNode{
			Kind: errorKindProvider,
			Provider: &genmodelgateway.ProviderErrorDetail{
				Provider:   providerErr.Provider(),
				Operation:  providerErr.Operation(),
				HTTPStatus: int64(providerErr.HTTPStatus()),
				Kind:       string(providerErr.Kind()),
				Code:       providerErr.Code(),
				Message:    providerErr.Message(),
				RequestID:  providerErr.RequestID(),
				Retryable:  providerErr.Retryable(),
			},
			CauseIndex: -1,
		}
		*nodes = append(*nodes, node)
		rememberEncodedErrorNode(err, idx, seen)
		node.CauseIndex = encodeErrorNode(errors.Unwrap(providerErr), nodes, seen)
		return idx
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		idx := int64(len(*nodes))
		node := &genmodelgateway.GatewayErrorNode{
			Kind:       errorKindJoined,
			CauseIndex: -1,
		}
		*nodes = append(*nodes, node)
		rememberEncodedErrorNode(err, idx, seen)
		for _, child := range joined.Unwrap() {
			if child != nil {
				if childIdx := encodeErrorNode(child, nodes, seen); childIdx >= 0 {
					node.ChildIndices = append(node.ChildIndices, childIdx)
				}
			}
		}
		return idx
	}
	if name, ok := model.UnadvertisedToolName(err); ok && errors.Unwrap(err) == nil {
		idx := int64(len(*nodes))
		*nodes = append(*nodes, &genmodelgateway.GatewayErrorNode{
			Kind:                 errorKindUnadvertised,
			UnadvertisedToolName: name,
			CauseIndex:           -1,
		})
		rememberEncodedErrorNode(err, idx, seen)
		return idx
	}
	//nolint:errorlint // Inspect exact node identity before recursing into cause.
	if toolName, cause, ok := model.MalformedToolArgumentsToolName(err); ok && errors.Unwrap(err) == cause {
		idx := int64(len(*nodes))
		node := &genmodelgateway.GatewayErrorNode{
			Kind:              errorKindMalformed,
			MalformedToolName: string(toolName),
			CauseIndex:        -1,
		}
		*nodes = append(*nodes, node)
		rememberEncodedErrorNode(err, idx, seen)
		node.CauseIndex = encodeErrorNode(cause, nodes, seen)
		return idx
	}
	idx := int64(len(*nodes))
	node := &genmodelgateway.GatewayErrorNode{
		Kind:       errorKindPlain,
		Message:    err.Error(),
		CauseIndex: -1,
	}
	*nodes = append(*nodes, node)
	rememberEncodedErrorNode(err, idx, seen)
	node.CauseIndex = encodeErrorNode(errors.Unwrap(err), nodes, seen)
	return idx
}

// findEncodedErrorNode returns the existing node index when err is comparable
// and has already been encoded in the current error graph.
func findEncodedErrorNode(err error, seen []encodedErrorNode) (int64, bool) {
	if err == nil || !reflect.TypeOf(err).Comparable() {
		return -1, false
	}
	for _, entry := range seen {
		//nolint:errorlint // Exact identity check for shared error DAG nodes.
		if entry.err == err {
			return entry.idx, true
		}
	}
	return -1, false
}

// rememberEncodedErrorNode records err at idx when err is a comparable value.
func rememberEncodedErrorNode(err error, idx int64, seen *[]encodedErrorNode) {
	if err == nil || !reflect.TypeOf(err).Comparable() {
		return
	}
	*seen = append(*seen, encodedErrorNode{err: err, idx: idx})
}

// fromGenError reconstructs a Go error from a Goa-generated GatewayError.
func fromGenError(in *genmodelgateway.GatewayError) error {
	if in == nil || len(in.Nodes) == 0 {
		return nil
	}
	memo := make([]error, len(in.Nodes))
	err := decodeErrorNode(in.Nodes, 0, memo)
	if err != nil && in.RetainedUsage != nil && in.Nodes[0] != nil && in.Nodes[0].Kind != errorKindValidation {
		if usage, uerr := fromGenUsage(in.RetainedUsage); uerr == nil {
			err = model.RetainUsage(err, usage)
		}
	}
	return err
}

// decodeErrorNode recursively reconstructs one error node from nodes[idx] while
// memoizing decoded nodes so shared indices decode to the same error instance.
func decodeErrorNode(nodes []*genmodelgateway.GatewayErrorNode, idx int64, memo []error) error {
	if idx < 0 || idx >= int64(len(nodes)) {
		return nil
	}
	if cached := memo[idx]; cached != nil {
		return cached
	}
	w := nodes[idx]
	if w == nil {
		return nil
	}
	decodeChild := func(childIdx int64) error {
		if childIdx <= idx || childIdx >= int64(len(nodes)) {
			return nil
		}
		return decodeErrorNode(nodes, childIdx, memo)
	}
	var decoded error
	switch w.Kind {
	case errorKindSentinel:
		if err := fromSentinelCode(w.Sentinel); err != nil {
			decoded = err
		} else {
			decoded = errors.New(w.Message)
		}
	case errorKindRequestValidation:
		cause := decodeChild(w.CauseIndex)
		if cause == nil {
			cause = errors.New(w.Message)
		}
		decoded = model.NewRequestValidationError(cause)
	case errorKindProvider:
		if w.Provider == nil {
			decoded = errors.New("gateway: malformed provider error")
			break
		}
		provider := w.Provider.Provider
		if provider == "" {
			provider = "gateway"
		}
		kind := model.ProviderErrorKind(w.Provider.Kind)
		if kind == "" {
			kind = model.ProviderErrorKindUnknown
		}
		httpStatus, err := int64ToInt(w.Provider.HTTPStatus, "provider.http_status")
		if err != nil {
			decoded = err
			break
		}
		decoded = model.NewProviderError(
			provider,
			w.Provider.Operation,
			httpStatus,
			kind,
			w.Provider.Code,
			w.Provider.Message,
			w.Provider.RequestID,
			w.Provider.Retryable,
			decodeChild(w.CauseIndex),
		)
	case errorKindValidation:
		if w.Validation == nil {
			decoded = errors.New("gateway: malformed output validation error")
			break
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
			u, err := fromGenUsage(w.Validation.Usage)
			if err != nil {
				decoded = err
				break
			}
			usage = &u
		}
		restored, err := model.RestoreOutputValidationError(
			model.OutputValidationKind(w.Validation.Kind),
			cause,
			ev,
			usage,
		)
		if err != nil {
			decoded = err
			break
		}
		if w.Validation.RecoveryCorrection != "" {
			correctable, err := model.RestoreCorrectableOutputValidationError(
				restored,
				w.Validation.RecoveryCorrection,
			)
			if err != nil {
				decoded = err
				break
			}
			decoded = correctable
			break
		}
		decoded = restored
	case errorKindUnadvertised:
		if w.UnadvertisedToolName != "" {
			decoded = model.NewUnadvertisedToolNameError(w.UnadvertisedToolName)
		} else {
			decoded = errors.New("model returned an unadvertised tool name")
		}
	case errorKindMalformed:
		cause := decodeChild(w.CauseIndex)
		if cause == nil {
			cause = errors.New("invalid JSON")
		}
		if w.MalformedToolName != "" {
			decoded = model.NewMalformedToolArgumentsError(tools.Ident(w.MalformedToolName), cause)
		} else {
			decoded = cause
		}
	case errorKindJoined:
		children := make([]error, 0, len(w.ChildIndices))
		for _, childIdx := range w.ChildIndices {
			if child := decodeChild(childIdx); child != nil {
				children = append(children, child)
			}
		}
		decoded = errors.Join(children...)
	default:
		cause := decodeChild(w.CauseIndex)
		switch {
		case cause == nil:
			decoded = errors.New(w.Message)
		case w.Message == "" || w.Message == cause.Error():
			decoded = cause
		default:
			prefix := strings.TrimSuffix(w.Message, ": "+cause.Error())
			decoded = fmt.Errorf("%s: %w", prefix, cause)
		}
	}
	memo[idx] = decoded
	return decoded
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
	case model.ErrRequestByteCapacity:
		return sentinelRequestByteCapacity, true
	case model.ErrImageSourceCapacity:
		return sentinelImageSourceCapacity, true
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
	case sentinelRequestByteCapacity:
		return model.ErrRequestByteCapacity
	case sentinelImageSourceCapacity:
		return model.ErrImageSourceCapacity
	case sentinelContextCanceled:
		return context.Canceled
	case sentinelContextDeadlineExceeded:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
