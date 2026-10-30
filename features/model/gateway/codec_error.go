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
			out.RetainedUsageIncomplete = model.UsageIncompleteFromError(err)
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
			UsageIncomplete:    validationErr.UsageIncomplete(),
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
		cause := errors.Unwrap(validationErr)
		for {
			//nolint:errorlint // Unwrap RetainUsage copies that point to the original OutputValidationError.
			innerValidation, isInner := cause.(*model.OutputValidationError)
			if !isInner || innerValidation == nil {
				break
			}
			cause = errors.Unwrap(innerValidation)
		}
		node.CauseIndex = encodeErrorNode(cause, nodes, seen)
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
		if len(node.ChildIndices) == 0 {
			node.Kind = errorKindPlain
			node.Message = err.Error()
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
// A non-nil GatewayError represents a remote failure; malformed or cyclic node
// graphs return a non-nil protocol error instead of silently succeeding.
func fromGenError(in *genmodelgateway.GatewayError) error {
	if in == nil {
		return nil
	}
	if err := validateErrorGraph(in.Nodes); err != nil {
		return fmt.Errorf("gateway: invalid wire error graph: %w", err)
	}
	if in.RetainedUsageIncomplete && in.RetainedUsage == nil {
		return errors.New("gateway: invalid wire error graph: retained_usage_incomplete requires retained_usage")
	}
	if in.RetainedUsage != nil && in.Nodes[0].Kind == errorKindValidation {
		return errors.New("gateway: invalid wire error graph: validation root node cannot carry top-level retained_usage")
	}
	memo := make([]error, len(in.Nodes))
	err := decodeErrorNode(in.Nodes, 0, memo)
	if err == nil {
		return errors.New("gateway: wire error decoded to nil")
	}
	if in.RetainedUsage != nil {
		u, uerr := fromGenUsage(in.RetainedUsage)
		if uerr != nil {
			return fmt.Errorf("gateway: invalid wire error usage: %w", uerr)
		}
		if in.RetainedUsageIncomplete {
			err = model.RetainIncompleteUsage(err, u)
		} else {
			err = model.RetainUsage(err, u)
		}
	}
	return err
}

// validateErrorGraph verifies that nodes forms a non-empty, bounds-safe,
// acyclic DAG with valid node discriminators and fields before decodeErrorNode
// traverses it.
func validateErrorGraph(nodes []*genmodelgateway.GatewayErrorNode) error {
	if len(nodes) == 0 {
		return errors.New("empty error node list")
	}
	const (
		stateUnvisited uint8 = iota
		stateVisiting
		stateVisited
	)
	state := make([]uint8, len(nodes))
	var visit func(idx int64) error
	visit = func(idx int64) error {
		if idx < 0 || idx >= int64(len(nodes)) {
			return fmt.Errorf("node index %d out of bounds", idx)
		}
		switch state[idx] {
		case stateVisiting:
			return fmt.Errorf("cycle detected at node %d", idx)
		case stateVisited:
			return nil
		}
		w := nodes[idx]
		if err := validateErrorNode(idx, w, int64(len(nodes))); err != nil {
			return err
		}
		state[idx] = stateVisiting
		if w.Kind == errorKindJoined {
			for _, childIdx := range w.ChildIndices {
				if err := visit(childIdx); err != nil {
					return err
				}
			}
		} else if w.CauseIndex >= 0 {
			if err := visit(w.CauseIndex); err != nil {
				return err
			}
		}
		state[idx] = stateVisited
		return nil
	}
	for i := range nodes {
		if state[i] == stateUnvisited {
			if err := visit(int64(i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateErrorNode verifies one wire error node's discriminator, edge indices,
// and kind-specific payload fields.
func validateErrorNode(idx int64, w *genmodelgateway.GatewayErrorNode, numNodes int64) error {
	if w == nil {
		return fmt.Errorf("node %d is nil", idx)
	}
	if w.Kind == errorKindJoined {
		if w.CauseIndex != -1 {
			return fmt.Errorf("joined node %d cannot have cause_index %d", idx, w.CauseIndex)
		}
		if len(w.ChildIndices) == 0 {
			return fmt.Errorf("joined node %d has no children", idx)
		}
		if w.Message != "" || w.Sentinel != "" || w.Provider != nil || w.Validation != nil ||
			w.UnadvertisedToolName != "" || w.MalformedToolName != "" {
			return fmt.Errorf("joined node %d has contradictory fields", idx)
		}
		return nil
	}
	if len(w.ChildIndices) > 0 {
		return fmt.Errorf("non-joined node %d cannot have child_indices", idx)
	}
	if w.CauseIndex < -1 || w.CauseIndex >= numNodes {
		return fmt.Errorf("node %d cause_index %d out of bounds", idx, w.CauseIndex)
	}
	switch w.Kind {
	case errorKindPlain:
		if w.Sentinel != "" || w.Provider != nil || w.Validation != nil ||
			w.UnadvertisedToolName != "" || w.MalformedToolName != "" {
			return fmt.Errorf("plain node %d has contradictory fields", idx)
		}
		if w.CauseIndex >= 0 && w.Message == "" {
			return fmt.Errorf("plain wrapper node %d requires non-empty message", idx)
		}
	case errorKindSentinel:
		if fromSentinelCode(w.Sentinel) == nil {
			return fmt.Errorf("sentinel node %d has unknown sentinel %q", idx, w.Sentinel)
		}
		if w.CauseIndex != -1 || w.Message != "" || w.Provider != nil || w.Validation != nil ||
			w.UnadvertisedToolName != "" || w.MalformedToolName != "" {
			return fmt.Errorf("sentinel node %d has contradictory fields", idx)
		}
	case errorKindRequestValidation:
		if w.Sentinel != "" || w.Provider != nil || w.Validation != nil ||
			w.UnadvertisedToolName != "" || w.MalformedToolName != "" {
			return fmt.Errorf("request_validation node %d has contradictory fields", idx)
		}
	case errorKindProvider:
		if w.Provider == nil || w.Message != "" || w.Sentinel != "" || w.Validation != nil ||
			w.UnadvertisedToolName != "" || w.MalformedToolName != "" {
			return fmt.Errorf("provider node %d has invalid fields", idx)
		}
	case errorKindValidation:
		if w.Validation == nil || w.Message != "" || w.Sentinel != "" || w.Provider != nil ||
			w.UnadvertisedToolName != "" || w.MalformedToolName != "" {
			return fmt.Errorf("validation node %d has invalid fields", idx)
		}
		if w.Validation.UsageIncomplete && w.Validation.Usage == nil {
			return fmt.Errorf("validation node %d has usage_incomplete without usage", idx)
		}
	case errorKindUnadvertised:
		if w.UnadvertisedToolName == "" || w.CauseIndex != -1 || w.Message != "" || w.Sentinel != "" ||
			w.Provider != nil || w.Validation != nil || w.MalformedToolName != "" {
			return fmt.Errorf("unadvertised_tool node %d has invalid fields", idx)
		}
	case errorKindMalformed:
		if w.MalformedToolName == "" || w.Message != "" || w.Sentinel != "" ||
			w.Provider != nil || w.Validation != nil || w.UnadvertisedToolName != "" {
			return fmt.Errorf("malformed_tool_args node %d has invalid fields", idx)
		}
	default:
		return fmt.Errorf("node %d has unknown kind %q", idx, w.Kind)
	}
	return nil
}

// decodeErrorNode recursively reconstructs one error node from nodes[idx] while
// memoizing decoded nodes so shared DAG indices decode to the same error instance.
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
		if childIdx < 0 || childIdx >= int64(len(nodes)) {
			return nil
		}
		return decodeErrorNode(nodes, childIdx, memo)
	}
	var decoded error
	switch w.Kind {
	case errorKindSentinel:
		decoded = fromSentinelCode(w.Sentinel)
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
			restored = correctable
		}
		if w.Validation.UsageIncomplete && usage != nil {
			decoded = model.RetainIncompleteUsage(restored, *usage)
			break
		}
		decoded = restored
	case errorKindUnadvertised:
		decoded = model.NewUnadvertisedToolNameError(w.UnadvertisedToolName)
	case errorKindMalformed:
		cause := decodeChild(w.CauseIndex)
		if cause == nil {
			cause = errors.New("invalid JSON")
		}
		decoded = model.NewMalformedToolArgumentsError(tools.Ident(w.MalformedToolName), cause)
	case errorKindJoined:
		children := make([]error, 0, len(w.ChildIndices))
		for _, childIdx := range w.ChildIndices {
			if child := decodeChild(childIdx); child != nil {
				children = append(children, child)
			}
		}
		decoded = errors.Join(children...)
	case errorKindPlain:
		cause := decodeChild(w.CauseIndex)
		switch {
		case cause == nil:
			decoded = errors.New(w.Message)
		case w.Message == cause.Error():
			decoded = fmt.Errorf("%w", cause)
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
