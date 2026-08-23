// Package temporalerrors tells Temporal which failures it may retry. It also
// reads failure details after Temporal stores and returns them.
package temporalerrors

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"

	"go.temporal.io/sdk/temporal"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

const (
	providerErrorApplicationType       = "goa_ai.provider_error"
	outputContractErrorApplicationType = "goa_ai.output_contract_error"
	invalidReservedApplicationType     = "goa_ai.invalid_reserved_error"
	genericErrorApplicationType        = "goa_ai.generic_error"

	providerErrorDetailsVersion = "goa_ai.provider_error.v1"
	genericErrorDetailsVersion  = "goa_ai.generic_error.v1"

	maxProviderBytes             = 128
	maxProviderOperationBytes    = 128
	maxProviderCodeBytes         = 256
	maxProviderMessageBytes      = 2048
	maxProviderRequestIDBytes    = 256
	maxTemporalErrorMessageBytes = 3072
	maxTemporalDetailsJSONBytes  = 4096
	maxEvidenceReplacementBytes  = 192
	maxGenericErrorMessageBytes  = 512
	maxGenericErrorTypeBytes     = 128
	maxClassificationDepth       = 64
	maxClassificationVisits      = 256
)

type (
	errorKind uint8

	// boundedText stores either the exact value or deterministic evidence for
	// an oversized value. Oversized source text never enters Temporal.
	boundedText struct {
		Value         string
		SHA256        string
		OriginalBytes int
	}

	// providerErrorDetails keeps model-service facts after Temporal stores an
	// activity failure.
	providerErrorDetails struct {
		Version    string
		Provider   boundedText
		Operation  boundedText
		HTTPStatus int
		Kind       string
		Code       boundedText
		Message    boundedText
		RequestID  boundedText
		Retryable  bool
	}

	// outputContractErrorDetails preserves which completed value failed after
	// Temporal serializes the terminal error.
	outputContractErrorDetails struct {
		Origin string
	}

	// genericErrorDetails retains bounded evidence for errors that do not use a
	// model or planner failure contract.
	genericErrorDetails struct {
		Version      string
		OriginalType boundedText
		Message      boundedText
		Retryable    bool
	}

	classification struct {
		kind        errorKind
		provider    *model.ProviderError
		origin      planner.OutputContractOrigin
		application *temporal.ApplicationError
		invalid     error
	}

	errorIdentity struct {
		typ reflect.Type
		ptr uintptr
	}
)

const (
	errorKindNone errorKind = iota
	errorKindProvider
	errorKindOutputContract
	errorKindInvalidReserved
	errorKindGeneric
)

// Wrap tells Temporal whether it may run the failed operation again. Direct
// output contract errors must not be retried. Model-service failures keep the
// retry setting reported by that service.
func Wrap(err error) error {
	if err == nil {
		return nil
	}
	// A direct custom Temporal ApplicationError owns retryability but not an
	// unbounded type, message, details payload, or cause.
	//nolint:errorlint // Only the exact top-level Temporal error owns retryability here.
	if appErr, ok := err.(*temporal.ApplicationError); ok && !reservedApplicationType(appErr.Type()) {
		return wrapGeneric(appErr, appErr.Type(), !appErr.NonRetryable())
	}
	// Preserve the exact invalid envelope emitted by an earlier Wrap call.
	//nolint:errorlint // Wrapped errors must still be classified from their outer owner below.
	if appErr, ok := err.(*temporal.ApplicationError); ok && appErr.Type() == invalidReservedApplicationType {
		return err
	}
	classified := classify(err)
	switch classified.kind {
	case errorKindOutputContract:
		//nolint:errorlint // Only the exact Temporal envelope is already serialized.
		if classified.application != nil && classified.application == err {
			return err
		}
		return temporal.NewNonRetryableApplicationError(
			boundedErrorMessage("output contract error", err.Error()),
			outputContractErrorApplicationType,
			nil,
			outputContractErrorDetails{Origin: string(classified.origin)},
		)
	case errorKindProvider:
		//nolint:errorlint // Only the exact Temporal envelope is already serialized.
		if classified.application != nil && classified.application == err {
			return err
		}
		details := providerDetails(classified.provider)
		if validationErr := validateProviderDetails(details); validationErr != nil {
			return wrapInvalidReserved(validationErr)
		}
		message := boundedErrorMessage("provider error", providerErrorMessage(details))
		if classified.provider.Retryable() {
			return temporal.NewApplicationError(
				message,
				providerErrorApplicationType,
				details,
			)
		}
		return temporal.NewNonRetryableApplicationError(
			message,
			providerErrorApplicationType,
			nil,
			details,
		)
	case errorKindInvalidReserved:
		//nolint:errorlint // Only the exact bounded Temporal envelope is already serialized.
		if classified.application != nil && classified.application == err {
			return err
		}
		return wrapInvalidReserved(classified.invalid)
	case errorKindGeneric:
		return classified.application
	case errorKindNone:
		return wrapGeneric(err, "", true)
	default:
		panic(fmt.Sprintf("temporalerrors: unknown classification kind %d", classified.kind))
	}
}

// IsOutputContract reports whether err is a direct output-contract failure or
// the same failure returned by Temporal.
func IsOutputContract(err error) bool {
	return classify(err).kind == errorKindOutputContract
}

// OutputContractOrigin returns the component whose output failed.
func OutputContractOrigin(err error) planner.OutputContractOrigin {
	classified := classify(err)
	if classified.kind != errorKindOutputContract {
		return ""
	}
	return classified.origin
}

// Provider returns model-service failure details from err or from the same
// failure returned by Temporal.
func Provider(err error) (*model.ProviderError, bool) {
	classified := classify(err)
	if classified.kind != errorKindProvider {
		return nil, false
	}
	return classified.provider, true
}

// classify walks outer errors before their children so the component that
// deliberately wrapped another failure keeps ownership of classification. The
// iterative walk bounds malformed graphs and detects pointer cycles.
func classify(err error) classification {
	if err == nil {
		return classification{}
	}
	type visit struct {
		err      error
		depth    int
		leaving  bool
		identity errorIdentity
	}
	stack := []visit{{err: err}}
	active := make(map[errorIdentity]struct{})
	visits := 0
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.leaving {
			delete(active, current.identity)
			continue
		}
		if current.err == nil {
			continue
		}
		visits++
		if visits > maxClassificationVisits || current.depth > maxClassificationDepth {
			return invalidReserved("error graph exceeds classification bounds")
		}
		if identity, ok := classificationIdentity(current.err); ok {
			if _, exists := active[identity]; exists {
				return invalidReserved("error graph contains a cycle")
			}
			active[identity] = struct{}{}
			stack = append(stack, visit{leaving: true, identity: identity})
		}
		if classified := classifyCurrent(current.err); classified.kind != errorKindNone {
			return classified
		}
		children, childErr := unwrapChildren(current.err)
		if childErr != nil {
			return invalidReserved("cannot traverse error graph: %v", childErr)
		}
		for index := len(children) - 1; index >= 0; index-- {
			stack = append(stack, visit{err: children[index], depth: current.depth + 1})
		}
	}
	return classification{}
}

// classifyCurrent recognizes one graph node without following its children.
func classifyCurrent(err error) classification {
	//nolint:errorlint // Classification intentionally gives the outer error ownership before children.
	switch current := err.(type) {
	case *model.ProviderError:
		return classification{
			kind:     errorKindProvider,
			provider: current,
		}
	case *planner.OutputContractError:
		if !validOutputOrigin(current.Origin()) {
			return invalidReserved("native output contract error has invalid origin %q", current.Origin())
		}
		return classification{
			kind:   errorKindOutputContract,
			origin: current.Origin(),
		}
	case *temporal.ApplicationError:
		switch current.Type() {
		case outputContractErrorApplicationType:
			return classifyOutputApplication(current)
		case providerErrorApplicationType:
			return classifyProviderApplication(current)
		case invalidReservedApplicationType:
			return classifyInvalidApplication(current)
		case genericErrorApplicationType:
			return classifyGenericApplication(current)
		}
	}
	return classification{}
}

// classificationIdentity returns a stable identity for reference-shaped error
// nodes. Value errors cannot form a reference cycle by themselves and remain
// bounded by the visit limit.
func classificationIdentity(err error) (errorIdentity, bool) {
	value := reflect.ValueOf(err)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		if value.IsNil() {
			return errorIdentity{}, false
		}
		return errorIdentity{typ: value.Type(), ptr: value.Pointer()}, true
	default:
		return errorIdentity{}, false
	}
}

// unwrapChildren reads either standard unwrap shape and converts panics from a
// malformed implementation into a bounded invalid-envelope result.
func unwrapChildren(err error) (children []error, resultErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			children = nil
			resultErr = fmt.Errorf("unwrap panicked")
		}
	}()
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return joined.Unwrap(), nil
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return []error{wrapped.Unwrap()}, nil
	}
	return nil, nil
}

// classifyOutputApplication validates the complete reserved Temporal envelope.
func classifyOutputApplication(appErr *temporal.ApplicationError) classification {
	if !appErr.NonRetryable() {
		return invalidReserved("reserved output contract error is retryable")
	}
	if appErr.Unwrap() != nil {
		return invalidReserved("reserved output contract error has a cause")
	}
	if err := validateTemporalMessage(appErr.Message()); err != nil {
		return invalidReserved("reserved output contract error message: %v", err)
	}
	var details outputContractErrorDetails
	if err := decodeApplicationDetails(appErr, &details); err != nil {
		return invalidReserved("reserved output contract error details: %v", err)
	}
	origin := planner.OutputContractOrigin(details.Origin)
	if !validOutputOrigin(origin) {
		return invalidReserved("reserved output contract error has invalid origin %q", details.Origin)
	}
	return classification{
		kind:        errorKindOutputContract,
		origin:      origin,
		application: appErr,
	}
}

// classifyProviderApplication validates saved provider details before calling
// the constructor that requires a provider and kind.
func classifyProviderApplication(appErr *temporal.ApplicationError) classification {
	if appErr.Unwrap() != nil {
		return invalidReserved("reserved provider error has a cause")
	}
	if err := validateTemporalMessage(appErr.Message()); err != nil {
		return invalidReserved("reserved provider error message: %v", err)
	}
	var details providerErrorDetails
	if err := decodeApplicationDetails(appErr, &details); err != nil {
		return invalidReserved("reserved provider error details: %v", err)
	}
	if err := validateProviderDetails(details); err != nil {
		return invalidReserved("reserved provider error details: %v", err)
	}
	expectedMessage := boundedErrorMessage("provider error", providerErrorMessage(details))
	if appErr.Message() != expectedMessage {
		return invalidReserved("reserved provider error message does not match its details")
	}
	kind := model.ProviderErrorKind(details.Kind)
	if appErr.NonRetryable() == details.Retryable {
		return invalidReserved(
			"reserved provider error retryable detail %t conflicts with nonretryable property %t",
			details.Retryable,
			appErr.NonRetryable(),
		)
	}
	return classification{
		kind: errorKindProvider,
		provider: model.NewProviderError(
			details.Provider.Value,
			details.Operation.Value,
			details.HTTPStatus,
			kind,
			details.Code.Value,
			details.Message.Value,
			details.RequestID.Value,
			details.Retryable,
			nil,
		),
		application: appErr,
	}
}

// classifyInvalidApplication accepts only the bounded, cause-free envelope
// emitted by Wrap. A malformed instance is replaced by a new bounded envelope.
func classifyInvalidApplication(appErr *temporal.ApplicationError) classification {
	if !appErr.NonRetryable() {
		return invalidReserved("reserved invalid-envelope error is retryable")
	}
	if err := validateTemporalMessage(appErr.Message()); err != nil {
		return invalidReserved("reserved invalid-envelope error message: %v", err)
	}
	if appErr.HasDetails() {
		return invalidReserved("reserved invalid-envelope error has details")
	}
	if appErr.Unwrap() != nil {
		return invalidReserved("reserved invalid-envelope error has a cause")
	}
	return classification{
		kind:        errorKindInvalidReserved,
		invalid:     fmt.Errorf("%s", appErr.Message()),
		application: appErr,
	}
}

// classifyGenericApplication accepts only the bounded cause-free envelope
// emitted for ordinary Go and custom Temporal errors.
func classifyGenericApplication(appErr *temporal.ApplicationError) classification {
	if appErr.Unwrap() != nil {
		return invalidReserved("reserved generic error has a cause")
	}
	if appErr.Message() != "operation failed" {
		return invalidReserved("reserved generic error has invalid message")
	}
	var details genericErrorDetails
	if err := decodeApplicationDetails(appErr, &details); err != nil {
		return invalidReserved("reserved generic error details: %v", err)
	}
	if err := validateGenericDetails(details); err != nil {
		return invalidReserved("reserved generic error details: %v", err)
	}
	if appErr.NonRetryable() == details.Retryable {
		return invalidReserved(
			"reserved generic error retryable detail %t conflicts with nonretryable property %t",
			details.Retryable,
			appErr.NonRetryable(),
		)
	}
	return classification{
		kind:        errorKindGeneric,
		application: appErr,
	}
}

// decodeApplicationDetails converts Temporal decoder panics into malformed
// envelope errors at this persistence boundary.
func decodeApplicationDetails(appErr *temporal.ApplicationError, target any) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("decode details: %v", recovered)
		}
	}()
	return appErr.Details(target)
}

// wrapGeneric converts an ordinary failure into a bounded framework envelope.
func wrapGeneric(err error, originalType string, retryable bool) error {
	message := safeErrorText(err)
	//nolint:errorlint // Exact application errors expose their owned message separately from Error formatting.
	if appErr, ok := err.(*temporal.ApplicationError); ok {
		message = appErr.Message()
	}
	details := genericErrorDetails{
		Version:      genericErrorDetailsVersion,
		OriginalType: saveBoundedText("application_type", originalType, maxGenericErrorTypeBytes),
		Message:      saveBoundedText("message", message, maxGenericErrorMessageBytes),
		Retryable:    retryable,
	}
	if validationErr := validateGenericDetails(details); validationErr != nil {
		return wrapInvalidReserved(validationErr)
	}
	if retryable {
		return temporal.NewApplicationError(
			"operation failed",
			genericErrorApplicationType,
			details,
		)
	}
	return temporal.NewNonRetryableApplicationError(
		"operation failed",
		genericErrorApplicationType,
		nil,
		details,
	)
}

// safeErrorText converts a panicking Error method into fixed bounded evidence.
func safeErrorText(err error) (text string) {
	defer func() {
		if recover() != nil {
			text = "error message unavailable"
		}
	}()
	return err.Error()
}

// validateGenericDetails rejects forged generic envelopes and enforces their
// serialized size independently of Temporal's outer failure encoding.
func validateGenericDetails(details genericErrorDetails) error {
	if details.Version != genericErrorDetailsVersion {
		return fmt.Errorf("unsupported version %q", details.Version)
	}
	if err := validateBoundedText("application_type", details.OriginalType, maxGenericErrorTypeBytes); err != nil {
		return err
	}
	if err := validateBoundedText("message", details.Message, maxGenericErrorMessageBytes); err != nil {
		return err
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode details for size validation: %w", err)
	}
	if len(encoded) > maxTemporalDetailsJSONBytes {
		return fmt.Errorf(
			"encoded details use %d bytes, maximum is %d",
			len(encoded),
			maxTemporalDetailsJSONBytes,
		)
	}
	return nil
}

// providerDetails projects one validated provider error into Temporal details.
func providerDetails(providerErr *model.ProviderError) providerErrorDetails {
	return providerErrorDetails{
		Version:    providerErrorDetailsVersion,
		Provider:   saveBoundedText("provider", providerErr.Provider(), maxProviderBytes),
		Operation:  saveBoundedText("operation", providerErr.Operation(), maxProviderOperationBytes),
		HTTPStatus: providerErr.HTTPStatus(),
		Kind:       string(providerErr.Kind()),
		Code:       saveBoundedText("code", providerErr.Code(), maxProviderCodeBytes),
		Message:    saveBoundedText("message", providerErr.Message(), maxProviderMessageBytes),
		RequestID:  saveBoundedText("request_id", providerErr.RequestID(), maxProviderRequestIDBytes),
		Retryable:  providerErr.Retryable(),
	}
}

// validateProviderDetails rejects any persisted shape that was not produced by
// providerDetails, including forged or partial oversized-value evidence.
func validateProviderDetails(details providerErrorDetails) error {
	if details.Version != providerErrorDetailsVersion {
		return fmt.Errorf("unsupported version %q", details.Version)
	}
	if err := validateBoundedText("provider", details.Provider, maxProviderBytes); err != nil {
		return err
	}
	if details.Provider.Value == "" {
		return fmt.Errorf("provider is empty")
	}
	if err := validateBoundedText("operation", details.Operation, maxProviderOperationBytes); err != nil {
		return err
	}
	if details.HTTPStatus != 0 && (details.HTTPStatus < 100 || details.HTTPStatus > 599) {
		return fmt.Errorf("HTTP status %d is outside 100-599", details.HTTPStatus)
	}
	kind := model.ProviderErrorKind(details.Kind)
	if !validProviderKind(kind) {
		return fmt.Errorf("invalid kind %q", details.Kind)
	}
	if err := validateBoundedText("code", details.Code, maxProviderCodeBytes); err != nil {
		return err
	}
	if err := validateBoundedText("message", details.Message, maxProviderMessageBytes); err != nil {
		return err
	}
	if err := validateBoundedText("request_id", details.RequestID, maxProviderRequestIDBytes); err != nil {
		return err
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode details for size validation: %w", err)
	}
	if len(encoded) > maxTemporalDetailsJSONBytes {
		return fmt.Errorf(
			"encoded details use %d bytes, maximum is %d",
			len(encoded),
			maxTemporalDetailsJSONBytes,
		)
	}
	return nil
}

// saveBoundedText retains small values exactly and replaces oversized values
// with a fixed-size message whose hash and byte count preserve audit evidence.
func saveBoundedText(field, value string, limit int) boundedText {
	if len(value) <= limit {
		return boundedText{Value: value}
	}
	sum := sha256.Sum256([]byte(value))
	hash := hex.EncodeToString(sum[:])
	size := len(value)
	return boundedText{
		Value:         boundedTextReplacement(field, hash, size),
		SHA256:        hash,
		OriginalBytes: size,
	}
}

// validateBoundedText verifies both exact values and oversized replacements.
func validateBoundedText(field string, saved boundedText, limit int) error {
	switch {
	case saved.SHA256 == "" && saved.OriginalBytes == 0:
		if len(saved.Value) > limit {
			return fmt.Errorf("%s uses %d bytes, maximum is %d", field, len(saved.Value), limit)
		}
		return nil
	case saved.SHA256 == "" || saved.OriginalBytes == 0:
		return fmt.Errorf("%s has incomplete oversized-value evidence", field)
	case saved.OriginalBytes <= limit:
		return fmt.Errorf(
			"%s evidence size %d does not exceed limit %d",
			field,
			saved.OriginalBytes,
			limit,
		)
	case len(saved.SHA256) != sha256.Size*2:
		return fmt.Errorf("%s evidence hash has %d characters", field, len(saved.SHA256))
	}
	decoded, err := hex.DecodeString(saved.SHA256)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("%s evidence hash is not SHA-256", field)
	}
	if hex.EncodeToString(decoded) != saved.SHA256 {
		return fmt.Errorf("%s evidence hash is not canonical lowercase hex", field)
	}
	expected := boundedTextReplacement(field, saved.SHA256, saved.OriginalBytes)
	if len(expected) > maxEvidenceReplacementBytes {
		return fmt.Errorf(
			"%s replacement uses %d bytes, maximum is %d",
			field,
			len(expected),
			maxEvidenceReplacementBytes,
		)
	}
	if saved.Value != expected {
		return fmt.Errorf("%s replacement does not match its evidence", field)
	}
	return nil
}

// boundedTextReplacement is the only persisted representation of oversized
// source text.
func boundedTextReplacement(field, hash string, size int) string {
	return fmt.Sprintf(
		"%s exceeded its persisted byte limit; sha256=%s original_bytes=%d",
		field,
		hash,
		size,
	)
}

// providerErrorMessage renders the same stable public shape from bounded
// details without consulting the original ProviderError or its cause.
func providerErrorMessage(details providerErrorDetails) string {
	providerErr := model.NewProviderError(
		details.Provider.Value,
		details.Operation.Value,
		details.HTTPStatus,
		model.ProviderErrorKind(details.Kind),
		details.Code.Value,
		details.Message.Value,
		details.RequestID.Value,
		details.Retryable,
		nil,
	)
	return providerErr.Error()
}

// boundedErrorMessage keeps small messages exact and records deterministic
// evidence instead of truncating oversized text.
func boundedErrorMessage(label, message string) string {
	return saveBoundedText(label, message, maxTemporalErrorMessageBytes).Value
}

// validateTemporalMessage enforces the outer Temporal message budget.
func validateTemporalMessage(message string) error {
	if len(message) > maxTemporalErrorMessageBytes {
		return fmt.Errorf(
			"uses %d bytes, maximum is %d",
			len(message),
			maxTemporalErrorMessageBytes,
		)
	}
	return nil
}

// wrapInvalidReserved creates the terminal bounded envelope used for every
// malformed reserved error.
func wrapInvalidReserved(err error) error {
	return temporal.NewNonRetryableApplicationError(
		boundedErrorMessage("invalid Temporal error envelope", err.Error()),
		invalidReservedApplicationType,
		nil,
	)
}

// invalidReserved reports a malformed reserved envelope without classifying it
// as a retryable output or provider failure.
func invalidReserved(format string, args ...any) classification {
	return classification{
		kind:    errorKindInvalidReserved,
		invalid: fmt.Errorf("invalid Temporal error envelope: "+format, args...),
	}
}

// validOutputOrigin reports whether an output failure names its actual owner.
func validOutputOrigin(origin planner.OutputContractOrigin) bool {
	return origin == planner.OutputContractOriginModel ||
		origin == planner.OutputContractOriginPlanner
}

// validProviderKind reports whether persisted details use the public provider
// failure vocabulary.
func validProviderKind(kind model.ProviderErrorKind) bool {
	switch kind {
	case model.ProviderErrorKindAuth,
		model.ProviderErrorKindInvalidRequest,
		model.ProviderErrorKindRateLimited,
		model.ProviderErrorKindUnavailable,
		model.ProviderErrorKindUnknown:
		return true
	default:
		return false
	}
}

// reservedApplicationType reports whether typ belongs to this package.
func reservedApplicationType(typ string) bool {
	return typ == providerErrorApplicationType ||
		typ == outputContractErrorApplicationType ||
		typ == invalidReservedApplicationType ||
		typ == genericErrorApplicationType
}
