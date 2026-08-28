package toolregistry

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

type serviceFailureError struct {
	error
}

type correctingServiceFailureError struct {
	error
	providerIssues   []*tools.FieldIssue
	validationIssues []*tools.FieldIssue
}

func (e *serviceFailureError) ToolFailure(tools.Ident) *planner.ToolFailure {
	return &planner.ToolFailure{
		Kind:  planner.FailureDomainRejection,
		Error: planner.ToolErrorFromError(e.error),
		Recovery: planner.RecoveryDirective{
			Action: planner.RecoveryFinish,
		},
	}
}

func (e *correctingServiceFailureError) ToolFailure(tools.Ident) *planner.ToolFailure {
	return &planner.ToolFailure{
		Kind:  planner.FailureInvalidCall,
		Error: planner.ToolErrorFromError(e.error),
		Recovery: planner.RecoveryDirective{
			Action: planner.RecoveryCorrectCall,
			Issues: tools.CloneFieldIssues(e.providerIssues),
		},
	}
}

func (e *correctingServiceFailureError) Issues() []*tools.FieldIssue {
	return tools.CloneFieldIssues(e.validationIssues)
}

func TestDeriveToolUseIDIsStableAndCollisionScoped(t *testing.T) {
	t.Parallel()

	first := DeriveToolUseID("run-a", "call-1")
	assert.Equal(t, first, DeriveToolUseID("run-a", "call-1"))
	assert.NotEqual(t, first, DeriveToolUseID("run-b", "call-1"))
	assert.NotEqual(t, DeriveToolUseID("ab", "c"), DeriveToolUseID("a", "bc"))
	require.NoError(t, ValidateRegistrationToken(first))
}

func TestValidateAdmissionIdentityRejectsNUL(t *testing.T) {
	t.Parallel()

	require.Error(t, ValidateToolUseID("call\x00alias"))
	message := NewToolCallMessage(
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"tool-use",
		time.Now().Add(MaxToolCallWait),
		time.Now().Add(DefaultResultStreamTTL),
		"tools.lookup",
		json.RawMessage(`{}`),
		&ToolCallMeta{
			RunID:      "run\x00alias",
			SessionID:  "session",
			ToolCallID: "call",
		},
	)
	require.ErrorContains(t, ValidateToolCallMessage(message), "run ID must not contain NUL")
}

func TestValidateToolCallMessageBoundsEveryMetadataID(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		field  string
		assign func(*ToolCallMeta, string)
	}{
		{name: "run", field: "run ID", assign: func(meta *ToolCallMeta, value string) { meta.RunID = value }},
		{name: "session", field: "session ID", assign: func(meta *ToolCallMeta, value string) { meta.SessionID = value }},
		{name: "turn", field: "turn ID", assign: func(meta *ToolCallMeta, value string) { meta.TurnID = value }},
		{name: "tool call", field: "tool call ID", assign: func(meta *ToolCallMeta, value string) { meta.ToolCallID = value }},
		{
			name:  "parent tool call",
			field: "parent tool call ID",
			assign: func(meta *ToolCallMeta, value string) {
				meta.ParentToolCallID = value
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := validToolCallMessage()
			test.assign(message.Meta, strings.Repeat("x", MaxToolCallMetaIDLength+1))
			require.ErrorContains(t, ValidateToolCallMessage(message), test.field+" exceeds 256 bytes")
		})
	}

	message := validToolCallMessage()
	boundary := strings.Repeat("x", MaxToolCallMetaIDLength)
	message.Meta = &ToolCallMeta{
		RunID:            boundary,
		SessionID:        boundary,
		TurnID:           boundary,
		ToolCallID:       boundary,
		ParentToolCallID: boundary,
	}
	require.NoError(t, ValidateToolCallMessage(message))
}

func TestValidateToolCallMessageLeavesExpirationToRegistry(t *testing.T) {
	t.Parallel()

	message := NewToolCallMessage(
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"tool-use",
		time.Now().Add(-2*time.Hour),
		time.Now().Add(-time.Hour),
		"tools.lookup",
		json.RawMessage(`{}`),
		&ToolCallMeta{
			RunID:      "run",
			SessionID:  "session",
			ToolCallID: "call",
		},
	)
	require.NoError(t, ValidateToolCallMessage(message))
}

func validToolCallMessage() ToolCallMessage {
	return NewToolCallMessage(
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"tool-use",
		time.Now().Add(MaxToolCallWait),
		time.Now().Add(DefaultResultStreamTTL),
		"tools.lookup",
		json.RawMessage(`{}`),
		&ToolCallMeta{
			RunID:      "run",
			SessionID:  "session",
			ToolCallID: "call",
		},
	)
}

func TestValidateResultStreamExpiration(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		expiresAt time.Time
		wantError bool
	}{
		{name: "positive milliseconds", expiresAt: time.UnixMilli(1)},
		{name: "historical remains structurally valid", expiresAt: time.UnixMilli(1000)},
		{name: "zero", expiresAt: time.Time{}, wantError: true},
		{name: "before Unix epoch", expiresAt: time.UnixMilli(-1), wantError: true},
		{name: "sub-millisecond precision", expiresAt: time.UnixMilli(1).Add(time.Nanosecond), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateResultStreamExpiration(test.expiresAt)
			if test.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateToolCallRefUsesStructuralDeadlinesOnly(t *testing.T) {
	t.Parallel()

	require.NoError(t, ValidateToolCallRef(ToolCallRef{
		ToolUseID:             "tool-use",
		RegistrationToken:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ExecutionDeadline:     time.UnixMilli(1),
		ResultStreamExpiresAt: time.UnixMilli(2),
	}))
	require.Error(t, ValidateToolCallRef(ToolCallRef{
		ToolUseID:             "tool-use",
		RegistrationToken:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ExecutionDeadline:     time.UnixMilli(2),
		ResultStreamExpiresAt: time.UnixMilli(1),
	}))
}

func TestDecodeServerDataPreservesTypedPayload(t *testing.T) {
	t.Parallel()

	items, err := DecodeServerData([]byte(`[{"kind":"records.citations","audience":"evidence","data":[{"index":1}]}]`))
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "records.citations", items[0].Kind)
	assert.Equal(t, "evidence", items[0].Audience)
	assert.JSONEq(t, `[{"index":1}]`, string(items[0].Data))
}

func TestNewToolResultServiceErrorMessagePreservesFailure(t *testing.T) {
	t.Parallel()

	result := NewToolResultServiceErrorMessage(
		"registration",
		"tool-use",
		"catalog.lookup.find_records",
		"invalid_input",
		&serviceFailureError{error: errors.New("history unavailable")},
	)

	require.NotNil(t, result.Error)
	require.NotNil(t, result.Error.Failure)
	assert.Equal(t, planner.FailureDomainRejection, result.Error.Failure.Kind)
	assert.Equal(t, planner.RecoveryFinish, result.Error.Failure.Recovery.Action)
}

func TestNewToolResultServiceErrorMessagePreservesProviderIssues(t *testing.T) {
	t.Parallel()

	providerIssue := &tools.FieldIssue{Field: "site", Constraint: "invalid_enum_value"}
	validationIssue := &tools.FieldIssue{Field: "query", Constraint: "missing_field"}
	result := NewToolResultServiceErrorMessage(
		"registration",
		"tool-use",
		"catalog.lookup.find_records",
		"invalid_arguments",
		&correctingServiceFailureError{
			error:            errors.New("invalid call"),
			providerIssues:   []*tools.FieldIssue{providerIssue},
			validationIssues: []*tools.FieldIssue{validationIssue},
		},
	)

	require.NotNil(t, result.Error)
	require.NotNil(t, result.Error.Failure)
	require.Len(t, result.Error.Failure.Recovery.Issues, 1)
	assert.Equal(t, "site", result.Error.Failure.Recovery.Issues[0].Field)
	assert.NotSame(t, providerIssue, result.Error.Failure.Recovery.Issues[0])
}

func TestNewToolResultServiceErrorMessageEnrichesMissingCorrectionIssues(t *testing.T) {
	t.Parallel()

	validationIssue := &tools.FieldIssue{Field: "query", Constraint: "missing_field"}
	result := NewToolResultServiceErrorMessage(
		"registration",
		"tool-use",
		"catalog.lookup.find_records",
		"invalid_arguments",
		&correctingServiceFailureError{
			error:            errors.New("invalid call"),
			validationIssues: []*tools.FieldIssue{validationIssue},
		},
	)

	require.NotNil(t, result.Error)
	require.NotNil(t, result.Error.Failure)
	require.Len(t, result.Error.Failure.Recovery.Issues, 1)
	assert.Equal(t, "query", result.Error.Failure.Recovery.Issues[0].Field)
	assert.NotSame(t, validationIssue, result.Error.Failure.Recovery.Issues[0])
}

func TestToolResultRetryMessageJSONContainsOnlyRetryControl(t *testing.T) {
	t.Parallel()

	message := NewToolResultRetryMessage(
		"1111111111111111111111111111111111111111111111111111111111111111",
		"tool-use",
		ToolRetryReasonProviderOverloaded,
		250*time.Millisecond,
	)
	body, err := json.Marshal(message)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"registration_token":"1111111111111111111111111111111111111111111111111111111111111111",
		"tool_use_id":"tool-use",
		"retry":{"reason":"provider_overloaded","retry_after_ms":250}
	}`, string(body))

	var decoded ToolResultMessage
	require.NoError(t, json.Unmarshal(body, &decoded))
	require.NoError(t, ValidateToolResultMessage(decoded))
	require.NotNil(t, decoded.Retry)
	assert.Nil(t, decoded.Error)
}
