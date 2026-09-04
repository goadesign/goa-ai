// Package model keeps generated response validators attached to the exact
// request contracts that selected them. Provider adapters cannot see or replace
// these in-process checks.
package model

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"goa.design/goa-ai/runtime/agent/internal/correction"
	"goa.design/goa-ai/runtime/agent/internal/responseevidence"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// RequestContract is the immutable output-validation contract copied from
	// one model request before provider work begins.
	RequestContract struct {
		stream             streamValidationContract
		completionValidate func(*Response, *Completion) error
		structuredValidate func(rawjson.Message) error
		toolValidators     map[tools.Ident]toolCallValidator
		toolChoiceMode     ToolChoiceMode
		toolChoiceName     tools.Ident
	}

	// OutputValidationError reports provider output that failed the immutable
	// contract captured before inference began. Kind names the first rejecting
	// check. The error retains only bounded response identity plus an optional
	// framework-owned rejected response.
	OutputValidationError struct {
		kind       OutputValidationKind
		cause      error
		evidence   ResponseEvidence
		rejected   *Response
		usage      *TokenUsage
		correction string
		restored   bool
	}

	streamValidationContract struct {
		modelClass              ModelClass
		structuredOutputPresent bool
		structuredOutputName    string
		toolChoiceMode          ToolChoiceMode
		toolChoiceName          string
	}

	toolCallValidator func(ToolCall) error

	// preparedRequestContract binds a compiled contract to the exact private
	// request copy that model.Client passes to its raw provider.
	preparedRequestContract struct {
		request  *Request
		contract *RequestContract
	}

	// toolCallValidationError carries framework-authored correction guidance
	// without retaining the generated codec message, which may quote rejected
	// provider values.
	toolCallValidationError struct {
		toolName   tools.Ident
		correction string
	}
)

// NewRequestContract validates request and copies every value used to accept
// provider output. Later request mutation cannot replace these checks.
func NewRequestContract(request *Request) (*RequestContract, error) {
	if err := preflightRequest(request); err != nil {
		return nil, err
	}
	return newRequestContract(request)
}

// newRequestContract builds a contract after the framework has already checked
// the request's combined input size and collection count.
func newRequestContract(request *Request) (*RequestContract, error) {
	if err := validateRequest(request); err != nil {
		return nil, err
	}
	if prepared := request.preparedContract; prepared != nil && prepared.request == request {
		return prepared.contract, nil
	}
	var structuredValidate func(rawjson.Message) error
	if request.StructuredOutput != nil {
		var err error
		structuredValidate, err = compileJSONSchemaValidator(request.StructuredOutput.Schema)
		if err != nil {
			return nil, fmt.Errorf("model request structured output schema: %w", err)
		}
	}
	validators, err := configuredToolCallValidators(request)
	if err != nil {
		return nil, err
	}
	choiceMode, choiceName, err := validateToolChoice(request.ToolChoice, validators)
	if err != nil {
		return nil, err
	}
	if err := validateInferenceMode(request); err != nil {
		return nil, err
	}
	return &RequestContract{
		stream:             newStreamValidationContract(request),
		completionValidate: request.completionValidate,
		structuredValidate: structuredValidate,
		toolValidators:     validators,
		toolChoiceMode:     choiceMode,
		toolChoiceName:     choiceName,
	}, nil
}

// preparedRequest binds the compiled contract while constructing the exact
// request value passed to a raw provider. The returned request is not mutated
// after construction. Other copies and direct provider calls compile their own
// contracts because their request identity does not match this value.
func preparedRequest(request *Request, contract *RequestContract) *Request {
	prepared := *request
	prepared.preparedContract = &preparedRequestContract{
		request:  &prepared,
		contract: contract,
	}
	return &prepared
}

// ValidateResponse owns one provider response, applies this immutable request
// contract, and returns the copy that callers may safely retain.
func (c *RequestContract) ValidateResponse(response *Response) (*Response, error) {
	evidence := ResponseEvidence{Present: response != nil}
	usage := c.validatedUsageEvidence(response)
	if response != nil {
		if err := validateTokenUsage(response.Usage); err != nil {
			return nil, newOutputValidationError(OutputValidationUsage, err, evidence, nil, usage)
		}
	}
	if err := preflightResponse(response, &dynamicValueWalk{}, dynamicCloneEvidence); err != nil {
		return nil, newOutputValidationError(
			requiredOutputValidationKind(err),
			err,
			evidence,
			nil,
			usage,
		)
	}
	evidence = responseEvidencePreflighted(response)
	owned, err := ownPreflightedResponse(response)
	if err != nil {
		return nil, newOutputValidationError(OutputValidationResponseShape, err, evidence, nil, usage)
	}
	if owned != nil {
		c.stampUsageIdentity(&owned.Usage)
	}
	if kind, err := c.validateOwnedResponse(owned); err != nil {
		return nil, newOutputValidationError(kind, err, evidence, owned, usage)
	}
	return owned, nil
}

// RejectProviderOutput classifies a translation failure after the provider
// returned output that could not be represented as a canonical Response.
// usage is the provider metadata extracted before translating content. Valid
// usage survives the rejection; response evidence records only that output was
// present because no canonical Response exists to fingerprint.
func (c *RequestContract) RejectProviderOutput(
	kind OutputValidationKind,
	usage *TokenUsage,
	cause error,
) *OutputValidationError {
	return newOutputValidationError(
		kind,
		cause,
		ResponseEvidence{Present: true},
		nil,
		c.validatedUsageValue(usage),
	)
}

// RejectResponse classifies a provider response translation failure as model
// output validation. Providers call it only after transport succeeded and the
// returned value could not be represented as a canonical Response.
func (c *RequestContract) RejectResponse(
	kind OutputValidationKind,
	response *Response,
	cause error,
) *OutputValidationError {
	if cause == nil {
		panic("model: output validation error requires a cause")
	}
	evidence := ResponseEvidence{Present: response != nil}
	usage := c.validatedUsageEvidence(response)
	if err := preflightResponse(response, &dynamicValueWalk{}, dynamicCloneEvidence); err != nil {
		return newOutputValidationError(kind, errors.Join(cause, err), evidence, nil, usage)
	}
	evidence = responseEvidencePreflighted(response)
	owned, err := ownPreflightedResponse(response)
	if err != nil {
		cause = errors.Join(cause, err)
		owned = nil
	}
	return newOutputValidationError(kind, cause, evidence, owned, usage)
}

// EvidenceForResponse returns a bounded fingerprint for one complete response.
// Planners use the response itself to identify rejected output; the runtime
// stores this evidence instead of carrying the response through the workflow.
func EvidenceForResponse(response *Response) ResponseEvidence {
	return responseEvidencePreflighted(response)
}

// Error returns a stable summary without rendering the rejected output or its
// validation cause. Callers use the typed accessors and errors.Is/As when they
// need structured diagnostic evidence.
func (e *OutputValidationError) Error() string {
	return "model output does not meet its request contract"
}

// Kind returns the closed category of the first request-contract check that
// rejected the provider output. The value contains no model or provider text.
func (e *OutputValidationError) Kind() OutputValidationKind {
	return e.kind
}

// Unwrap exposes the underlying output-validation cause.
func (e *OutputValidationError) Unwrap() error {
	return e.cause
}

// Evidence returns bounded identity captured from the rejected response.
func (e *OutputValidationError) Evidence() ResponseEvidence {
	return e.evidence
}

// RejectedResponse returns an isolated copy of an optional terminal rejection.
// Recoverable tool-call errors never retain their rejected response.
func (e *OutputValidationError) RejectedResponse() (*Response, error) {
	return cloneResponseForValidation(e.rejected)
}

// Usage returns validated scalar token usage retained independently of the
// rejected response body.
func (e *OutputValidationError) Usage() *TokenUsage {
	if e.usage == nil {
		return nil
	}
	usage := *e.usage
	return &usage
}

// RecoveryCorrection returns framework-authored guidance when an advertised
// tool input contract identified a safe way to replace the rejected invocation.
// The guidance contains no provider payload or submitted values. An empty
// string means the rejection remains terminal.
func (e *OutputValidationError) RecoveryCorrection() string {
	if e == nil {
		return ""
	}
	return e.correction
}

// RestoreOutputValidationError reconstructs a model-output rejection after a
// trusted transport decodes its bounded evidence. It rejects inconsistent
// evidence, usage, or a cause already classified as another model failure.
// The restored error is terminal; callers may attach separately transported
// correction guidance with RestoreCorrectableOutputValidationError.
func RestoreOutputValidationError(
	kind OutputValidationKind,
	cause error,
	evidence ResponseEvidence,
	usage *TokenUsage,
) (*OutputValidationError, error) {
	if !validOutputValidationKind(kind) {
		return nil, fmt.Errorf("model: restore output validation error: invalid kind %q", kind)
	}
	if err := validateRestoredOutputValidationCause(cause); err != nil {
		return nil, err
	}
	if err := validateResponseEvidence(evidence); err != nil {
		return nil, fmt.Errorf("model: restore output validation error: %w", err)
	}
	if usage != nil {
		if err := validateTokenUsage(*usage); err != nil {
			return nil, fmt.Errorf("model: restore output validation error usage: %w", err)
		}
		cloned := *usage
		usage = &cloned
	}
	return &OutputValidationError{
		kind:     kind,
		cause:    cause,
		evidence: evidence,
		usage:    usage,
		restored: true,
	}, nil
}

// RestoreCorrectableOutputValidationError attaches separately transported
// correction guidance to a terminal error returned by
// RestoreOutputValidationError. This two-step contract prevents provider,
// cancellation, and other arbitrary errors from being classified as
// correctable in one call. It preserves the restored cause, evidence, and usage
// without retaining rejected model output.
func RestoreCorrectableOutputValidationError(
	restored *OutputValidationError,
	recoveryCorrection string,
) (*OutputValidationError, error) {
	if restored == nil {
		return nil, errors.New("model: correctable output validation error requires a restored terminal error")
	}
	if !restored.restored {
		return nil, errors.New(
			"model: correctable output validation error requires an error returned by RestoreOutputValidationError",
		)
	}
	if restored.correction != "" {
		return nil, errors.New("model: correctable output validation error requires a terminal restored error")
	}
	if recoveryCorrection == "" {
		return nil, errors.New("model: correctable output validation error requires correction guidance")
	}
	if !utf8.ValidString(recoveryCorrection) {
		return nil, errors.New("model: correctable output validation error correction must be valid UTF-8")
	}
	if strings.TrimSpace(recoveryCorrection) == "" {
		return nil, errors.New("model: correctable output validation error correction must not be blank")
	}
	if len(recoveryCorrection) > correction.MaxBytes {
		return nil, fmt.Errorf(
			"model: correctable output validation error correction exceeds %d bytes",
			correction.MaxBytes,
		)
	}
	return &OutputValidationError{
		kind:       restored.kind,
		cause:      restored.cause,
		evidence:   restored.evidence,
		usage:      restored.Usage(),
		correction: recoveryCorrection,
		restored:   true,
	}, nil
}

// validateRestoredOutputValidationCause rejects error types whose existing
// classification contradicts a transported model-output rejection. Other
// causes retain their exact identity so errors.Is and errors.As keep working.
func validateRestoredOutputValidationCause(cause error) error {
	if isNilInterface(cause) {
		if cause == nil {
			return errors.New("model: output validation error requires a cause")
		}
		return errors.New("model: output validation error cause must not be typed nil")
	}
	switch {
	case errors.Is(cause, ErrStreamingUnsupported):
		return errors.New("model: output validation error cause must not contain ErrStreamingUnsupported")
	case errors.Is(cause, ErrStructuredOutputUnsupported):
		return errors.New("model: output validation error cause must not contain ErrStructuredOutputUnsupported")
	case errors.Is(cause, ErrRateLimited):
		return errors.New("model: output validation error cause must not contain ErrRateLimited")
	case errors.Is(cause, ErrEmptyStream):
		return errors.New("model: output validation error cause must not contain ErrEmptyStream")
	case errors.Is(cause, ErrTokenCountingUnsupported):
		return errors.New("model: output validation error cause must not contain ErrTokenCountingUnsupported")
	}
	var outputValidationErr *OutputValidationError
	if errors.As(cause, &outputValidationErr) {
		return errors.New("model: output validation error cause must not contain OutputValidationError")
	}
	var providerErr *ProviderError
	if errors.As(cause, &providerErr) {
		return errors.New("model: output validation error cause must not contain ProviderError")
	}
	if errors.Is(cause, context.Canceled) {
		return errors.New("model: output validation error cause must not contain context cancellation")
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return errors.New("model: output validation error cause must not contain context deadline")
	}
	return nil
}

// validateOwnedResponse applies the request rules after the model boundary has
// copied the provider response and stamped caller-owned usage identity.
func (c *RequestContract) validateOwnedResponse(response *Response) (OutputValidationKind, error) {
	if kind, err := validateCanonicalResponseOutput(response); err != nil {
		return kind, fmt.Errorf("invalid model response: %w", err)
	}
	if c.stream.structuredOutputPresent {
		payload, err := structuredOutputResponsePayload(response)
		if err != nil {
			return OutputValidationStructuredOutput, err
		}
		if err := c.structuredValidate(payload); err != nil {
			return OutputValidationStructuredOutput, fmt.Errorf(
				"structured output response does not match its schema: %w",
				err,
			)
		}
	}
	if c.completionValidate != nil {
		if err := c.completionValidate(response, nil); err != nil {
			return OutputValidationStructuredOutput, err
		}
	}
	if err := validateConfiguredToolCalls(c.toolValidators, response); err != nil {
		if _, ok := UnadvertisedToolName(err); ok {
			return OutputValidationToolIdentity, err
		}
		return OutputValidationToolArguments, err
	}
	if err := validateToolChoiceResponse(c.toolChoiceMode, c.toolChoiceName, response); err != nil {
		return OutputValidationToolChoice, err
	}
	return "", nil
}

// stampUsageIdentity applies the logical model class copied from the request.
// Provider model identity remains empty when the provider did not report it.
func (c *RequestContract) stampUsageIdentity(usage *TokenUsage) {
	if c.stream.modelClass != "" {
		usage.ModelClass = c.stream.modelClass
	}
}

// validatedUsageEvidence snapshots nonnegative counts before response
// traversal. Invalid provider model identity is omitted from rejected evidence,
// while a valid concrete provider model is preserved.
func (c *RequestContract) validatedUsageEvidence(response *Response) *TokenUsage {
	if response == nil {
		return nil
	}
	return c.validatedUsageValue(&response.Usage)
}

// validatedUsageValue keeps valid bounded counts from rejected output. Missing
// provider identity remains empty; malformed identity is omitted.
func (c *RequestContract) validatedUsageValue(providerUsage *TokenUsage) *TokenUsage {
	if providerUsage == nil {
		return nil
	}
	usage := *providerUsage
	if usage.InputTokens < 0 ||
		usage.OutputTokens < 0 ||
		usage.TotalTokens < 0 ||
		usage.CacheReadTokens < 0 ||
		usage.CacheWriteTokens < 0 {
		return nil
	}
	if len(usage.Model) > maxTokenUsageModelBytes ||
		!utf8.ValidString(usage.Model) {
		usage.Model = ""
	}
	usage.ModelClass = c.stream.modelClass
	if err := validateTokenUsage(usage); err != nil {
		return nil
	}
	return &usage
}

// validateRequest checks shared request values before any provider can observe
// or narrow them.
func validateRequest(request *Request) error {
	if request == nil {
		return fmt.Errorf("model request is required")
	}
	if err := validateTokenUsage(TokenUsage{
		Model:      request.Model,
		ModelClass: request.ModelClass,
	}); err != nil {
		return fmt.Errorf("model request identity: %w", err)
	}
	if request.MaxTokens < 0 {
		return errors.New("model request max tokens cannot be negative")
	}
	if math.IsNaN(float64(request.Temperature)) || math.IsInf(float64(request.Temperature), 0) ||
		request.Temperature < 0 {
		return errors.New("model request temperature must be finite and non-negative")
	}
	if request.Thinking != nil && request.Thinking.BudgetTokens < 0 {
		return errors.New("model request thinking budget cannot be negative")
	}
	for index, message := range request.Messages {
		if err := validateRequestMessage(message); err != nil {
			return fmt.Errorf("model request message %d: %w", index, err)
		}
	}
	if output := request.StructuredOutput; output != nil {
		if output.Name == "" {
			return errors.New("model request structured output name is required")
		}
		if len(output.Schema) == 0 {
			return errors.New("model request structured output schema is required")
		}
		if !json.Valid(output.Schema) {
			return errors.New("model request structured output schema is not valid JSON")
		}
		if len(output.SchemaWithoutRootExample) > 0 && !json.Valid(output.SchemaWithoutRootExample) {
			return errors.New("model request structured output schema without root example is not valid JSON")
		}
		if len(output.ExampleJSON) > 0 && !json.Valid(output.ExampleJSON) {
			return errors.New("model request structured output example is not valid JSON")
		}
	}
	return nil
}

// validateInferenceMode keeps provider-enforced structured completion and tool
// calling as separate request modes.
func validateInferenceMode(request *Request) error {
	if request.StructuredOutput == nil {
		return nil
	}
	if len(request.Tools) > 0 {
		return errors.New("model request structured output cannot include tools")
	}
	if request.ToolChoice != nil {
		return errors.New("model request structured output cannot set tool choice")
	}
	return nil
}

// validateRequestMessage checks the provider-neutral transcript shape before an
// adapter applies provider-specific role and media restrictions.
func validateRequestMessage(message *Message) error {
	if message == nil {
		return errors.New("message is nil")
	}
	switch message.Role {
	case ConversationRoleSystem, ConversationRoleUser, ConversationRoleAssistant:
	default:
		return fmt.Errorf("message has unsupported role %q", message.Role)
	}
	if len(message.Parts) == 0 {
		return errors.New("message has no parts")
	}
	if _, err := message.MarshalJSON(); err != nil {
		return fmt.Errorf("message is not canonical: %w", err)
	}
	return nil
}

// validateToolChoice checks request shape and returns the immutable response
// constraint represented by choice.
func validateToolChoice(
	choice *ToolChoice,
	validators map[tools.Ident]toolCallValidator,
) (ToolChoiceMode, tools.Ident, error) {
	if choice == nil {
		return ToolChoiceModeAuto, "", nil
	}
	name := tools.Ident(choice.Name)
	switch choice.Mode {
	case "", ToolChoiceModeAuto:
		if name != "" {
			return "", "", errors.New("model request auto tool choice cannot name a tool")
		}
		return ToolChoiceModeAuto, "", nil
	case ToolChoiceModeNone:
		if name != "" {
			return "", "", errors.New("model request none tool choice cannot name a tool")
		}
		return ToolChoiceModeNone, "", nil
	case ToolChoiceModeAny:
		if name != "" {
			return "", "", errors.New("model request any tool choice cannot name a tool")
		}
		if len(validators) == 0 {
			return "", "", errors.New("model request any tool choice requires at least one tool")
		}
		return ToolChoiceModeAny, "", nil
	case ToolChoiceModeTool:
		if name == "" {
			return "", "", errors.New("model request named tool choice requires a tool name")
		}
		if _, ok := validators[name]; !ok {
			return "", "", fmt.Errorf("model request tool choice names unadvertised tool %q", name)
		}
		return ToolChoiceModeTool, name, nil
	default:
		return "", "", fmt.Errorf("model request has unsupported tool choice mode %q", choice.Mode)
	}
}

// configuredToolCallValidators snapshots the names and payload checks attached
// to the exact tool definitions advertised in request.
func configuredToolCallValidators(request *Request) (map[tools.Ident]toolCallValidator, error) {
	validators := make(map[tools.Ident]toolCallValidator, len(request.Tools))
	for _, definition := range request.Tools {
		if definition == nil {
			return nil, fmt.Errorf("model request contains a nil tool definition")
		}
		name := tools.Ident(definition.Name)
		if name == "" {
			return nil, errors.New("model request contains a tool definition with an empty name")
		}
		if _, exists := validators[name]; exists {
			return nil, fmt.Errorf("model request contains duplicate tool definition %q", name)
		}
		validate := definition.Input.validate
		fields := tools.CloneFieldMetadata(definition.Input.fields)
		if validate == nil {
			return nil, fmt.Errorf("model request tool %q has no payload validator", name)
		}
		if definition.NoArguments {
			if !definition.Input.acceptsNoArguments {
				return nil, fmt.Errorf("model request tool %q declares no arguments but its schema declares fields", name)
			}
			validators[name] = func(call ToolCall) error {
				if string(call.Payload) != "{}" {
					return fmt.Errorf("model tool %q payload is not the canonical empty object", call.Name)
				}
				return nil
			}
			continue
		}
		validators[name] = func(call ToolCall) error {
			if err := validate(call.Payload); err != nil {
				if !isToolInputContractRejection(err) {
					return fmt.Errorf("model tool %q payload failed its request contract: %w", call.Name, err)
				}
				return &toolCallValidationError{
					toolName:   call.Name,
					correction: toolInputCorrection(err, call.Payload, fields),
				}
			}
			return nil
		}
	}
	return validators, nil
}

// isToolInputContractRejection reports whether the advertised input schema or a
// typed payload validator rejected arguments before the tool ran. A plain codec
// error lacks that typed proof and therefore stays terminal.
func isToolInputContractRejection(err error) bool {
	// Inspect this exact node. Following wrapped errors here would let one
	// accepted child hide an unrelated failure in the same error tree.
	//nolint:errorlint
	switch rejection := err.(type) {
	case *tools.ValidationError:
		return rejection != nil
	case *advertisedInputValidationError:
		return rejection != nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !isToolInputContractRejection(child) {
				return false
			}
		}
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return isToolInputContractRejection(cause)
	}
	return false
}

// validateConfiguredToolCalls applies request-owned advertised payload checks
// after the provider-neutral response shape has passed validation.
func validateConfiguredToolCalls(
	validators map[tools.Ident]toolCallValidator,
	response *Response,
) error {
	for _, call := range response.ToolCalls() {
		validate, exists := validators[call.Name]
		if !exists {
			return fmt.Errorf(
				"model returned tool %q that was not present in its request: %w",
				call.Name,
				NewUnadvertisedToolNameError(string(call.Name)),
			)
		}
		if validate != nil {
			if err := validate(call); err != nil {
				return err
			}
		}
	}
	return nil
}

// Error reports which advertised tool contract rejected the call without
// retaining the validator message or provider arguments.
func (e *toolCallValidationError) Error() string {
	return fmt.Sprintf("model tool %q payload failed its request contract", e.toolName)
}

// modelRecoveryCorrection gives OutputValidationError the bounded safe
// guidance derived before the rejected payload leaves validation.
func (e *toolCallValidationError) modelRecoveryCorrection() string {
	return e.correction
}

// validateToolChoiceResponse enforces the exact tool-use constraint copied
// before inference.
func validateToolChoiceResponse(
	mode ToolChoiceMode,
	name tools.Ident,
	response *Response,
) error {
	calls := response.ToolCalls()
	switch mode {
	case ToolChoiceModeAuto:
		return nil
	case ToolChoiceModeNone:
		if len(calls) != 0 {
			return fmt.Errorf("model returned %d tool calls for tool choice none", len(calls))
		}
		return nil
	case ToolChoiceModeAny:
		if len(calls) == 0 {
			return errors.New("model returned no tool calls for tool choice any")
		}
		return nil
	case ToolChoiceModeTool:
		if len(calls) == 0 {
			return fmt.Errorf("model returned no tool calls for required tool %q", name)
		}
		for _, call := range calls {
			if call.Name != name {
				return fmt.Errorf("model returned tool %q when tool choice requires %q", call.Name, name)
			}
		}
		return nil
	default:
		panic(fmt.Sprintf("model: unsupported captured tool choice mode %q", mode))
	}
}

// structuredOutputResponsePayload extracts the unary equivalent of a final
// streaming completion. The request-owned schema validator parses and checks
// the returned bytes before callers can receive the response.
func structuredOutputResponsePayload(response *Response) (rawjson.Message, error) {
	if len(response.ToolCalls()) != 0 {
		return nil, errors.New("structured output response returned tool calls")
	}
	if len(response.Content) != 1 {
		return nil, fmt.Errorf(
			"structured output response requires exactly one assistant message, got %d",
			len(response.Content),
		)
	}
	message := response.Content[0]
	if message.Role != ConversationRoleAssistant {
		return nil, fmt.Errorf("structured output response contains %q message", message.Role)
	}
	var payload strings.Builder
	for _, part := range message.Parts {
		switch actual := part.(type) {
		case TextPart:
			payload.WriteString(actual.Text)
		case ThinkingPart, CacheCheckpointPart:
		default:
			return nil, fmt.Errorf("structured output response contains unsupported part %T", part)
		}
	}
	if payload.Len() == 0 {
		return nil, errors.New("structured output response did not contain assistant JSON")
	}
	return rawjson.Message(payload.String()), nil
}

// newStreamValidationContract copies every request value that changes stream
// acceptance. Later request mutation therefore cannot change an existing
// validated stream.
func newStreamValidationContract(request *Request) streamValidationContract {
	contract := streamValidationContract{
		modelClass: request.ModelClass,
	}
	if request.ToolChoice != nil {
		contract.toolChoiceMode = request.ToolChoice.Mode
		contract.toolChoiceName = request.ToolChoice.Name
	}
	if request.StructuredOutput != nil {
		contract.structuredOutputPresent = true
		contract.structuredOutputName = request.StructuredOutput.Name
	}
	return contract
}

// validateResponseEvidence checks that optional response identity is either
// absent, present without a fingerprint, or a complete versioned fingerprint.
func validateResponseEvidence(evidence ResponseEvidence) error {
	if !evidence.Present {
		if evidence.Version != "" || evidence.SHA256 != "" || evidence.Size != 0 {
			return errors.New("response evidence without a response must be empty")
		}
		return nil
	}
	if evidence.SHA256 == "" {
		if evidence.Version != "" || evidence.Size != 0 {
			return errors.New("response evidence without a digest must not include a version or size")
		}
		return nil
	}
	if evidence.Version != responseevidence.VersionV1 &&
		evidence.Version != responseevidence.VersionV2 {
		return fmt.Errorf("response evidence has unsupported version %q", evidence.Version)
	}
	if len(evidence.SHA256) != 64 {
		return errors.New("response evidence SHA-256 must contain 64 hexadecimal characters")
	}
	if evidence.SHA256 != strings.ToLower(evidence.SHA256) {
		return errors.New("response evidence SHA-256 must use lowercase hexadecimal characters")
	}
	if _, err := hex.DecodeString(evidence.SHA256); err != nil {
		return fmt.Errorf("response evidence SHA-256: %w", err)
	}
	if evidence.Size <= 0 {
		return errors.New("response evidence with a digest must have a positive size")
	}
	return nil
}

// responseEvidence computes bounded identity without retaining provider-owned
// response data. Fingerprinting failures leave the optional digest empty.
func responseEvidencePreflighted(response *Response) ResponseEvidence {
	evidence := ResponseEvidence{Present: response != nil}
	if response == nil {
		return evidence
	}
	fingerprint, err := fingerprintPreflightedResponse(response)
	if err == nil {
		evidence.Version = responseevidence.VersionV2
		evidence.SHA256 = fingerprint.sha256
		evidence.Size = fingerprint.size
	}
	return evidence
}

// newOutputValidationError stores only framework-owned rejected response data.
func newOutputValidationError(
	kind OutputValidationKind,
	cause error,
	evidence ResponseEvidence,
	rejected *Response,
	usage *TokenUsage,
) *OutputValidationError {
	if !validOutputValidationKind(kind) {
		panic(fmt.Sprintf("model: invalid output validation kind %q", kind))
	}
	if cause == nil {
		panic("model: output validation error requires a cause")
	}
	var malformed *malformedToolArgumentsError
	if errors.As(cause, &malformed) && kind != OutputValidationToolArguments {
		panic("model: malformed tool arguments require the tool_arguments validation kind")
	}
	correction := recoveryCorrectionFromError(cause)
	if correction != "" {
		rejected = nil
	}
	return &OutputValidationError{
		kind:       kind,
		cause:      cause,
		evidence:   evidence,
		rejected:   rejected,
		usage:      usage,
		correction: correction,
	}
}

// recoveryCorrectionFromError accepts correction guidance only when every
// error leaf describes the same correctable rejection. Any unrelated failure
// keeps the combined error terminal.
func recoveryCorrectionFromError(err error) string {
	// Inspect this exact node before walking every child. errors.As would accept
	// one correctable child while ignoring an unrelated failure beside it.
	//nolint:errorlint
	switch source := err.(type) {
	case *toolCallValidationError:
		if source == nil {
			return ""
		}
		return source.modelRecoveryCorrection()
	case *malformedToolArgumentsError:
		if source == nil {
			return ""
		}
		return source.modelRecoveryCorrection()
	case *OutputValidationError:
		if source == nil || !source.restored {
			return ""
		}
		return source.RecoveryCorrection()
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var correction string
		children := joined.Unwrap()
		if len(children) == 0 {
			return ""
		}
		for _, child := range children {
			childCorrection := recoveryCorrectionFromError(child)
			if childCorrection == "" || correction != "" && childCorrection != correction {
				return ""
			}
			correction = childCorrection
		}
		return correction
	}
	if cause := errors.Unwrap(err); cause != nil {
		return recoveryCorrectionFromError(cause)
	}
	return ""
}
