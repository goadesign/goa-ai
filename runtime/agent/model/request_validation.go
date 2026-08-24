// Package model keeps generated response validators attached to the exact
// request contracts that selected them. Provider adapters cannot see or replace
// these in-process checks.
package model

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"goa.design/goa-ai/runtime/agent/internal/responseevidence"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// RequestContract is the immutable output-validation contract copied from
	// one model request before provider work begins.
	RequestContract struct {
		stream             streamValidationContract
		completionValidate func(*Response, *Completion) error
		toolValidators     map[tools.Ident]toolCallValidator
		toolChoiceMode     ToolChoiceMode
		toolChoiceName     tools.Ident
	}

	// OutputValidationError reports provider output that failed the immutable
	// contract captured before inference began. It retains only bounded response
	// identity plus an optional framework-owned rejected response.
	OutputValidationError struct {
		cause    error
		evidence ResponseEvidence
		rejected *Response
		usage    *TokenUsage
	}

	streamValidationContract struct {
		modelClass              ModelClass
		structuredOutputPresent bool
		structuredOutputName    string
		toolChoiceMode          ToolChoiceMode
		toolChoiceName          string
	}

	toolCallValidator func(ToolCall) error
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
		toolValidators:     validators,
		toolChoiceMode:     choiceMode,
		toolChoiceName:     choiceName,
	}, nil
}

// ValidateResponse owns one provider response, applies this immutable request
// contract, and returns the copy that callers may safely retain.
func (c *RequestContract) ValidateResponse(response *Response) (*Response, error) {
	evidence := ResponseEvidence{Present: response != nil}
	usage := c.validatedUsageEvidence(response)
	if err := preflightResponse(response, &dynamicValueWalk{}, dynamicCloneEvidence); err != nil {
		return nil, newOutputValidationError(err, evidence, nil, usage)
	}
	evidence = responseEvidencePreflighted(response)
	owned, err := ownPreflightedResponse(response)
	if err != nil {
		return nil, newOutputValidationError(err, evidence, nil, usage)
	}
	if owned != nil {
		c.stampUsageIdentity(&owned.Usage)
	}
	if err := c.validateOwnedResponse(owned); err != nil {
		return nil, newOutputValidationError(err, evidence, owned, usage)
	}
	return owned, nil
}

// RejectProviderOutput classifies a translation failure after the provider
// returned output that could not be represented as a canonical Response.
// usage is the provider metadata extracted before translating content. Valid
// usage survives the rejection; response evidence records only that output was
// present because no canonical Response exists to fingerprint.
func (c *RequestContract) RejectProviderOutput(usage *TokenUsage, cause error) *OutputValidationError {
	if cause == nil {
		panic("model: output validation error requires a cause")
	}
	return newOutputValidationError(
		cause,
		ResponseEvidence{Present: true},
		nil,
		c.validatedUsageValue(usage),
	)
}

// RejectResponse classifies a provider response translation failure as model
// output validation. Providers call it only after transport succeeded and the
// returned value could not be represented as a canonical Response.
func (c *RequestContract) RejectResponse(response *Response, cause error) *OutputValidationError {
	if cause == nil {
		panic("model: output validation error requires a cause")
	}
	evidence := ResponseEvidence{Present: response != nil}
	usage := c.validatedUsageEvidence(response)
	if err := preflightResponse(response, &dynamicValueWalk{}, dynamicCloneEvidence); err != nil {
		return newOutputValidationError(errors.Join(cause, err), evidence, nil, usage)
	}
	evidence = responseEvidencePreflighted(response)
	owned, err := ownPreflightedResponse(response)
	if err != nil {
		cause = errors.Join(cause, err)
		owned = nil
	}
	return newOutputValidationError(cause, evidence, owned, usage)
}

// Error describes why provider output failed its request contract.
func (e *OutputValidationError) Error() string {
	return "model output does not meet its request contract: " + e.cause.Error()
}

// Unwrap exposes the underlying output-validation cause.
func (e *OutputValidationError) Unwrap() error {
	return e.cause
}

// Evidence returns bounded identity captured from the rejected response.
func (e *OutputValidationError) Evidence() ResponseEvidence {
	return e.evidence
}

// RejectedResponse returns an isolated copy of the optional rejected response.
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

// RestoreOutputValidationError reconstructs a model-output rejection after a
// trusted transport decodes its bounded evidence. It rejects inconsistent
// evidence or usage instead of accepting a forged validation result.
func RestoreOutputValidationError(cause error, evidence ResponseEvidence, usage *TokenUsage) (*OutputValidationError, error) {
	if cause == nil {
		return nil, errors.New("model: output validation error requires a cause")
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
	return newOutputValidationError(cause, evidence, nil, usage), nil
}

// validateOwnedResponse applies the request rules after the model boundary has
// copied the provider response and stamped caller-owned usage identity.
func (c *RequestContract) validateOwnedResponse(response *Response) error {
	if err := validateCanonicalResponse(response); err != nil {
		return fmt.Errorf("invalid model response: %w", err)
	}
	if c.stream.structuredOutputPresent {
		if err := validateStructuredOutputResponse(response); err != nil {
			return err
		}
	}
	if c.completionValidate != nil {
		if err := c.completionValidate(response, nil); err != nil {
			return err
		}
	}
	if err := validateConfiguredToolCalls(c.toolValidators, response); err != nil {
		return err
	}
	return validateToolChoiceResponse(c.toolChoiceMode, c.toolChoiceName, response)
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
		if len(output.Schema) > 0 && !json.Valid(output.Schema) {
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

// configuredToolCallValidators snapshots the names and generated payload checks
// attached to the exact tool definitions advertised in request. Caller-authored
// definitions remain valid entries with no generated check.
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
				return fmt.Errorf("model tool %q payload failed its request contract: %w", call.Name, err)
			}
			return nil
		}
	}
	return validators, nil
}

// validateConfiguredToolCalls applies request-owned generated payload checks
// after the provider-neutral response shape has passed validation.
func validateConfiguredToolCalls(
	validators map[tools.Ident]toolCallValidator,
	response *Response,
) error {
	for _, call := range response.ToolCalls() {
		validate, exists := validators[call.Name]
		if !exists {
			return fmt.Errorf("model returned tool %q that was not present in its request", call.Name)
		}
		if validate != nil {
			if err := validate(call); err != nil {
				return err
			}
		}
	}
	return nil
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

// validateStructuredOutputResponse requires the unary equivalent of the
// streaming completion envelope: one assistant message containing JSON and no
// tool calls. Generated completion decoding, when attached, runs afterward.
func validateStructuredOutputResponse(response *Response) error {
	if len(response.ToolCalls()) != 0 {
		return errors.New("structured output response returned tool calls")
	}
	if len(response.Content) != 1 {
		return fmt.Errorf(
			"structured output response requires exactly one assistant message, got %d",
			len(response.Content),
		)
	}
	message := response.Content[0]
	if message.Role != ConversationRoleAssistant {
		return fmt.Errorf("structured output response contains %q message", message.Role)
	}
	var payload strings.Builder
	for _, part := range message.Parts {
		switch actual := part.(type) {
		case TextPart:
			payload.WriteString(actual.Text)
		case ThinkingPart, CacheCheckpointPart:
		default:
			return fmt.Errorf("structured output response contains unsupported part %T", part)
		}
	}
	data := []byte(payload.String())
	if len(strings.TrimSpace(payload.String())) == 0 {
		return errors.New("structured output response did not contain assistant JSON")
	}
	if !json.Valid(data) {
		return errors.New("structured output response assistant payload is not valid JSON")
	}
	return nil
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
	if evidence.Version != responseevidence.VersionV1 {
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
		evidence.Version = responseevidence.VersionV1
		evidence.SHA256 = fingerprint.sha256
		evidence.Size = fingerprint.size
	}
	return evidence
}

// newOutputValidationError stores only framework-owned rejected response data.
func newOutputValidationError(
	cause error,
	evidence ResponseEvidence,
	rejected *Response,
	usage *TokenUsage,
) *OutputValidationError {
	return &OutputValidationError{
		cause:    cause,
		evidence: evidence,
		rejected: rejected,
		usage:    usage,
	}
}
