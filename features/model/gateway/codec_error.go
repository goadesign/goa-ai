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

// Sentinel discriminators carried inside GatewayErrorNodeDetail.
const (
	sentinelStreamingUnsupported        genmodelgateway.GatewayErrorNodeDetailBranchSentinel = "streaming_unsupported"
	sentinelStructuredOutputUnsupported genmodelgateway.GatewayErrorNodeDetailBranchSentinel = "structured_output_unsupported"
	sentinelToolSearchUnsupported       genmodelgateway.GatewayErrorNodeDetailBranchSentinel = "tool_search_unsupported"
	sentinelToolContentUnsupported      genmodelgateway.GatewayErrorNodeDetailBranchSentinel = "tool_content_unsupported"
	sentinelRateLimited                 genmodelgateway.GatewayErrorNodeDetailBranchSentinel = "rate_limited"
	sentinelEmptyStream                 genmodelgateway.GatewayErrorNodeDetailBranchSentinel = "empty_stream"
	sentinelTokenCountingUnsupported    genmodelgateway.GatewayErrorNodeDetailBranchSentinel = "token_counting_unsupported"
	sentinelRequestByteCapacity         genmodelgateway.GatewayErrorNodeDetailBranchSentinel = "request_byte_capacity"
	sentinelImageSourceCapacity         genmodelgateway.GatewayErrorNodeDetailBranchSentinel = "image_source_capacity"
	sentinelContextCanceled             genmodelgateway.GatewayErrorNodeDetailBranchSentinel = "context_canceled"
	sentinelContextDeadlineExceeded     genmodelgateway.GatewayErrorNodeDetailBranchSentinel = "context_deadline_exceeded"
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
	if usage := model.UsageFromError(err); usage != nil {
		incomplete := model.UsageIncompleteFromError(err)
		if !nodesCarryRetainedUsage(nodes, *usage, incomplete) {
			out.RetainedUsage = toGenUsage(*usage)
			out.RetainedUsageIncomplete = incomplete
		}
	}
	return out
}

// nodesCarryRetainedUsage reports whether the first usage-bearing
// ValidationErrorDetail node in nodes already carries usage and incomplete.
func nodesCarryRetainedUsage(
	nodes []*genmodelgateway.GatewayErrorNode,
	usage model.TokenUsage,
	incomplete bool,
) bool {
	for _, node := range nodes {
		val, ok := node.Detail.AsValidation()
		if !ok || val == nil || val.Usage == nil {
			continue
		}
		return *val.Usage == *toGenUsage(usage) && val.UsageIncomplete == incomplete
	}
	return false
}

// encodeErrorNode appends one error node and its descendants to nodes and
// returns the appended node's index (-1 when err is nil).
func encodeErrorNode(err error, nodes *[]*genmodelgateway.GatewayErrorNode, seen *[]encodedErrorNode) int64 {
	if err == nil {
		return -1
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
		node := &genmodelgateway.GatewayErrorNode{}
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
		detail.CauseIndex = encodeErrorNode(cause, nodes, seen)
		node.Detail = genmodelgateway.NewGatewayErrorNodeDetailValidation(detail)
		return idx
	}
	if sentinel, ok := sentinelCode(err); ok {
		idx := int64(len(*nodes))
		*nodes = append(*nodes, &genmodelgateway.GatewayErrorNode{
			Detail: genmodelgateway.NewGatewayErrorNodeDetailSentinel(sentinel),
		})
		rememberEncodedErrorNode(err, idx, seen)
		return idx
	}
	//nolint:errorlint // Inspect exact node before unwrapping children.
	if reqValidationErr, ok := err.(*model.RequestValidationError); ok && reqValidationErr != nil {
		idx := int64(len(*nodes))
		node := &genmodelgateway.GatewayErrorNode{}
		*nodes = append(*nodes, node)
		rememberEncodedErrorNode(err, idx, seen)
		node.Detail = genmodelgateway.NewGatewayErrorNodeDetailRequestValidation(&genmodelgateway.RequestValidationErrorDetail{
			CauseIndex: encodeErrorNode(errors.Unwrap(reqValidationErr), nodes, seen),
		})
		return idx
	}
	//nolint:errorlint // Inspect exact node before unwrapping children.
	if providerErr, ok := err.(*model.ProviderError); ok && providerErr != nil {
		idx := int64(len(*nodes))
		node := &genmodelgateway.GatewayErrorNode{}
		*nodes = append(*nodes, node)
		rememberEncodedErrorNode(err, idx, seen)
		node.Detail = genmodelgateway.NewGatewayErrorNodeDetailProvider(&genmodelgateway.ProviderErrorDetail{
			Provider:   providerErr.Provider(),
			Operation:  providerErr.Operation(),
			HTTPStatus: int64(providerErr.HTTPStatus()),
			Kind:       string(providerErr.Kind()),
			Code:       providerErr.Code(),
			Message:    providerErr.Message(),
			RequestID:  providerErr.RequestID(),
			Retryable:  providerErr.Retryable(),
			CauseIndex: encodeErrorNode(errors.Unwrap(providerErr), nodes, seen),
		})
		return idx
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		idx := int64(len(*nodes))
		node := &genmodelgateway.GatewayErrorNode{}
		*nodes = append(*nodes, node)
		rememberEncodedErrorNode(err, idx, seen)
		var childIndices []int64
		for _, child := range joined.Unwrap() {
			if child != nil {
				if childIdx := encodeErrorNode(child, nodes, seen); childIdx >= 0 {
					childIndices = append(childIndices, childIdx)
				}
			}
		}
		if len(childIndices) == 0 {
			node.Detail = genmodelgateway.NewGatewayErrorNodeDetailPlain(&genmodelgateway.PlainErrorDetail{
				Message:    err.Error(),
				CauseIndex: -1,
			})
		} else {
			node.Detail = genmodelgateway.NewGatewayErrorNodeDetailJoined(&genmodelgateway.JoinedErrorDetail{
				ChildIndices: childIndices,
			})
		}
		return idx
	}
	if name, ok := model.UnadvertisedToolName(err); ok && errors.Unwrap(err) == nil {
		idx := int64(len(*nodes))
		*nodes = append(*nodes, &genmodelgateway.GatewayErrorNode{
			Detail: genmodelgateway.NewGatewayErrorNodeDetailUnadvertisedTool(
				genmodelgateway.GatewayErrorNodeDetailBranchUnadvertisedTool(name),
			),
		})
		rememberEncodedErrorNode(err, idx, seen)
		return idx
	}
	//nolint:errorlint // Inspect exact node identity before recursing into cause.
	if toolName, cause, ok := model.MalformedToolArgumentsToolName(err); ok && errors.Unwrap(err) == cause {
		idx := int64(len(*nodes))
		node := &genmodelgateway.GatewayErrorNode{}
		*nodes = append(*nodes, node)
		rememberEncodedErrorNode(err, idx, seen)
		node.Detail = genmodelgateway.NewGatewayErrorNodeDetailMalformedToolArgs(&genmodelgateway.MalformedToolArgsErrorDetail{
			ToolName:   string(toolName),
			CauseIndex: encodeErrorNode(cause, nodes, seen),
		})
		return idx
	}
	// Transparently unwrap message-identical wrappers (such as model.RetainUsage)
	// whose usage is captured on the root GatewayError.
	if inner := errors.Unwrap(err); inner != nil && inner.Error() == err.Error() {
		return encodeErrorNode(inner, nodes, seen)
	}
	idx := int64(len(*nodes))
	node := &genmodelgateway.GatewayErrorNode{}
	*nodes = append(*nodes, node)
	rememberEncodedErrorNode(err, idx, seen)
	node.Detail = genmodelgateway.NewGatewayErrorNodeDetailPlain(&genmodelgateway.PlainErrorDetail{
		Message:    err.Error(),
		CauseIndex: encodeErrorNode(errors.Unwrap(err), nodes, seen),
	})
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
	if in.RetainedUsage != nil && in.Nodes[0].Detail.Kind() == genmodelgateway.GatewayErrorNodeDetailKindValidation {
		return errors.New("gateway: invalid wire error graph: validation root node cannot carry top-level retained_usage")
	}
	memo := make([]error, len(in.Nodes))
	err, decodeErr := decodeErrorNode(in.Nodes, 0, memo)
	if decodeErr != nil {
		return fmt.Errorf("gateway: invalid wire error graph: %w", decodeErr)
	}
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
	numNodes := int64(len(nodes))
	var visit func(idx int64) error
	visit = func(idx int64) error {
		if idx < 0 || idx >= numNodes {
			return fmt.Errorf("node index %d out of bounds", idx)
		}
		switch state[idx] {
		case stateVisiting:
			return fmt.Errorf("cycle detected at node %d", idx)
		case stateVisited:
			return nil
		}
		w := nodes[idx]
		if err := validateErrorNode(idx, w, numNodes); err != nil {
			return err
		}
		state[idx] = stateVisiting
		//nolint:exhaustive // Sentinel and UnadvertisedTool are leaf variants with no outgoing edges.
		switch w.Detail.Kind() {
		case genmodelgateway.GatewayErrorNodeDetailKindJoined:
			joined, _ := w.Detail.AsJoined()
			for _, childIdx := range joined.ChildIndices {
				if err := visit(childIdx); err != nil {
					return err
				}
			}
		case genmodelgateway.GatewayErrorNodeDetailKindPlain:
			plain, _ := w.Detail.AsPlain()
			if plain.CauseIndex >= 0 {
				if err := visit(plain.CauseIndex); err != nil {
					return err
				}
			}
		case genmodelgateway.GatewayErrorNodeDetailKindRequestValidation:
			reqVal, _ := w.Detail.AsRequestValidation()
			if err := visit(reqVal.CauseIndex); err != nil {
				return err
			}
		case genmodelgateway.GatewayErrorNodeDetailKindProvider:
			prov, _ := w.Detail.AsProvider()
			if prov.CauseIndex >= 0 {
				if err := visit(prov.CauseIndex); err != nil {
					return err
				}
			}
		case genmodelgateway.GatewayErrorNodeDetailKindValidation:
			val, _ := w.Detail.AsValidation()
			if err := visit(val.CauseIndex); err != nil {
				return err
			}
		case genmodelgateway.GatewayErrorNodeDetailKindMalformedToolArgs:
			malformed, _ := w.Detail.AsMalformedToolArgs()
			if err := visit(malformed.CauseIndex); err != nil {
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

// validateErrorNode verifies one wire error node's union branch, edge indices,
// and branch-specific invariants.
func validateErrorNode(idx int64, w *genmodelgateway.GatewayErrorNode, numNodes int64) error {
	if w == nil {
		return fmt.Errorf("node %d is nil", idx)
	}
	if err := w.Detail.Validate(); err != nil {
		return fmt.Errorf("node %d has invalid detail: %w", idx, err)
	}
	switch w.Detail.Kind() {
	case genmodelgateway.GatewayErrorNodeDetailKindPlain:
		plain, _ := w.Detail.AsPlain()
		if plain.CauseIndex < -1 || plain.CauseIndex >= numNodes {
			return fmt.Errorf("plain node %d cause_index %d out of bounds", idx, plain.CauseIndex)
		}
		if plain.CauseIndex >= 0 && plain.Message == "" {
			return fmt.Errorf("plain wrapper node %d requires non-empty message", idx)
		}
	case genmodelgateway.GatewayErrorNodeDetailKindSentinel:
		sentinel, _ := w.Detail.AsSentinel()
		if fromSentinelCode(sentinel) == nil {
			return fmt.Errorf("sentinel node %d has unknown sentinel %q", idx, sentinel)
		}
	case genmodelgateway.GatewayErrorNodeDetailKindRequestValidation:
		reqVal, _ := w.Detail.AsRequestValidation()
		if reqVal.CauseIndex < 0 || reqVal.CauseIndex >= numNodes {
			return fmt.Errorf("request_validation node %d cause_index %d out of bounds", idx, reqVal.CauseIndex)
		}
	case genmodelgateway.GatewayErrorNodeDetailKindProvider:
		prov, _ := w.Detail.AsProvider()
		if prov.CauseIndex < -1 || prov.CauseIndex >= numNodes {
			return fmt.Errorf("provider node %d cause_index %d out of bounds", idx, prov.CauseIndex)
		}
		if prov.Provider == "" {
			return fmt.Errorf("provider node %d requires non-empty provider", idx)
		}
		if prov.Kind == "" {
			return fmt.Errorf("provider node %d requires non-empty kind", idx)
		}
	case genmodelgateway.GatewayErrorNodeDetailKindValidation:
		val, _ := w.Detail.AsValidation()
		if val.CauseIndex < 0 || val.CauseIndex >= numNodes {
			return fmt.Errorf("validation node %d cause_index %d out of bounds", idx, val.CauseIndex)
		}
		if val.UsageIncomplete && val.Usage == nil {
			return fmt.Errorf("validation node %d has usage_incomplete without usage", idx)
		}
	case genmodelgateway.GatewayErrorNodeDetailKindUnadvertisedTool:
		name, _ := w.Detail.AsUnadvertisedTool()
		if name == "" {
			return fmt.Errorf("unadvertised_tool node %d requires non-empty tool name", idx)
		}
	case genmodelgateway.GatewayErrorNodeDetailKindMalformedToolArgs:
		malformed, _ := w.Detail.AsMalformedToolArgs()
		if malformed.ToolName == "" {
			return fmt.Errorf("malformed_tool_args node %d requires non-empty tool_name", idx)
		}
		if malformed.CauseIndex < 0 || malformed.CauseIndex >= numNodes {
			return fmt.Errorf("malformed_tool_args node %d cause_index %d out of bounds", idx, malformed.CauseIndex)
		}
	case genmodelgateway.GatewayErrorNodeDetailKindJoined:
		joined, _ := w.Detail.AsJoined()
		if len(joined.ChildIndices) == 0 {
			return fmt.Errorf("joined node %d has no children", idx)
		}
		for _, childIdx := range joined.ChildIndices {
			if childIdx < 0 || childIdx >= numNodes {
				return fmt.Errorf("joined node %d child_index %d out of bounds", idx, childIdx)
			}
		}
	default:
		return fmt.Errorf("node %d has unknown detail kind %q", idx, w.Detail.Kind())
	}
	return nil
}

// decodeErrorNode recursively reconstructs one error node from nodes[idx] while
// memoizing decoded nodes so shared DAG indices decode to the same error instance.
// Callers must run validateErrorGraph(nodes) first.
func decodeErrorNode(nodes []*genmodelgateway.GatewayErrorNode, idx int64, memo []error) (error, error) {
	if cached := memo[idx]; cached != nil {
		return cached, nil
	}
	w := nodes[idx]
	decodeOptionalChild := func(childIdx int64) (error, error) {
		if childIdx < 0 {
			return nil, nil
		}
		return decodeErrorNode(nodes, childIdx, memo)
	}
	var decoded error
	switch w.Detail.Kind() {
	case genmodelgateway.GatewayErrorNodeDetailKindSentinel:
		sentinel, _ := w.Detail.AsSentinel()
		decoded = fromSentinelCode(sentinel)
	case genmodelgateway.GatewayErrorNodeDetailKindRequestValidation:
		reqVal, _ := w.Detail.AsRequestValidation()
		cause, err := decodeErrorNode(nodes, reqVal.CauseIndex, memo)
		if err != nil {
			return nil, err
		}
		decoded = model.NewRequestValidationError(cause)
	case genmodelgateway.GatewayErrorNodeDetailKindProvider:
		prov, _ := w.Detail.AsProvider()
		httpStatus, err := int64ToInt(prov.HTTPStatus, "provider.http_status")
		if err != nil {
			return nil, err
		}
		cause, err := decodeOptionalChild(prov.CauseIndex)
		if err != nil {
			return nil, err
		}
		decoded = model.NewProviderError(
			prov.Provider,
			prov.Operation,
			httpStatus,
			model.ProviderErrorKind(prov.Kind),
			prov.Code,
			prov.Message,
			prov.RequestID,
			prov.Retryable,
			cause,
		)
	case genmodelgateway.GatewayErrorNodeDetailKindValidation:
		val, _ := w.Detail.AsValidation()
		cause, err := decodeErrorNode(nodes, val.CauseIndex, memo)
		if err != nil {
			return nil, err
		}
		var ev model.ResponseEvidence
		if val.Evidence != nil {
			ev = model.ResponseEvidence{
				Present: val.Evidence.Present,
				Version: val.Evidence.Version,
				SHA256:  val.Evidence.Sha256,
				Size:    val.Evidence.Size,
			}
		}
		var usage *model.TokenUsage
		if val.Usage != nil {
			u, err := fromGenUsage(val.Usage)
			if err != nil {
				return nil, err
			}
			usage = &u
		}
		restored, err := model.RestoreOutputValidationError(
			model.OutputValidationKind(val.Kind),
			cause,
			ev,
			usage,
		)
		if err != nil {
			return nil, err
		}
		if val.RecoveryCorrection != "" {
			correctable, err := model.RestoreCorrectableOutputValidationError(
				restored,
				val.RecoveryCorrection,
			)
			if err != nil {
				return nil, err
			}
			restored = correctable
		}
		if val.UsageIncomplete && usage != nil {
			decoded = model.RetainIncompleteUsage(restored, *usage)
		} else {
			decoded = restored
		}
	case genmodelgateway.GatewayErrorNodeDetailKindUnadvertisedTool:
		name, _ := w.Detail.AsUnadvertisedTool()
		decoded = model.NewUnadvertisedToolNameError(string(name))
	case genmodelgateway.GatewayErrorNodeDetailKindMalformedToolArgs:
		malformed, _ := w.Detail.AsMalformedToolArgs()
		cause, err := decodeErrorNode(nodes, malformed.CauseIndex, memo)
		if err != nil {
			return nil, err
		}
		decoded = model.NewMalformedToolArgumentsError(tools.Ident(malformed.ToolName), cause)
	case genmodelgateway.GatewayErrorNodeDetailKindJoined:
		joined, _ := w.Detail.AsJoined()
		children := make([]error, 0, len(joined.ChildIndices))
		for _, childIdx := range joined.ChildIndices {
			child, err := decodeErrorNode(nodes, childIdx, memo)
			if err != nil {
				return nil, err
			}
			children = append(children, child)
		}
		decoded = errors.Join(children...)
	case genmodelgateway.GatewayErrorNodeDetailKindPlain:
		plain, _ := w.Detail.AsPlain()
		cause, err := decodeOptionalChild(plain.CauseIndex)
		if err != nil {
			return nil, err
		}
		if cause == nil {
			decoded = errors.New(plain.Message)
		} else {
			prefix := strings.TrimSuffix(plain.Message, ": "+cause.Error())
			decoded = fmt.Errorf("%s: %w", prefix, cause)
		}
	}
	memo[idx] = decoded
	return decoded, nil
}

// sentinelCode returns the wire code for an exact sentinel error value.
func sentinelCode(err error) (genmodelgateway.GatewayErrorNodeDetailBranchSentinel, bool) {
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
func fromSentinelCode(code genmodelgateway.GatewayErrorNodeDetailBranchSentinel) error {
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
