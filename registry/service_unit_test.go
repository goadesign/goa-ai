package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	genregistrypb "goa.design/goa-ai/registry/gen/grpc/registry/pb"
	genregistryserver "goa.design/goa-ai/registry/gen/grpc/registry/server"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	goa "goa.design/goa/v3/pkg"
	streamopts "goa.design/pulse/streaming/options"
)

type (
	// recordingCallAdmissions captures the provider token selected by CallTool.
	recordingCallAdmissions struct {
		registrationToken string
		attached          *callAdmission
		initializations   int
	}

	// unitStreamManager records admitted publication without Redis.
	unitStreamManager struct {
		message       toolregistry.ToolCallMessage
		publishErrors []error
		publications  int
	}

	// unitHealthTracker reports healthy provider routing without background work.
	unitHealthTracker struct{}
)

func TestGeneratedCallToolRejectsMissingWireProtocolVersion(t *testing.T) {
	t.Parallel()

	request := &genregistrypb.CallToolRequest{
		Toolset:     "test.toolset",
		Tool:        "test.toolset.lookup",
		PayloadJson: []byte(`{}`),
		Meta: &genregistrypb.ToolCallMeta{
			RunId:      "run-1",
			SessionId:  "session-1",
			ToolCallId: "call-1",
		},
	}
	require.Error(t, genregistryserver.ValidateCallToolRequest(request))

	request.WireProtocolVersion = int32(toolregistry.WireProtocolVersion)
	require.NoError(t, genregistryserver.ValidateCallToolRequest(request))
}

func TestGeneratedRetryToolRequiresAdmissionFence(t *testing.T) {
	t.Parallel()

	request := &genregistrypb.RetryToolRequest{
		Toolset:             "test.toolset",
		Tool:                "test.toolset.lookup",
		PayloadJson:         []byte(`{}`),
		WireProtocolVersion: int32(toolregistry.WireProtocolVersion),
		Meta: &genregistrypb.ToolCallMeta{
			RunId:      "run-1",
			SessionId:  "session-1",
			ToolCallId: "call-1",
		},
	}
	require.Error(t, genregistryserver.ValidateRetryToolRequest(request))

	request.ExpectedRegistrationToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	require.NoError(t, genregistryserver.ValidateRetryToolRequest(request))
}

func TestCallToolRejectsMismatchedWireProtocolBeforeDependencies(t *testing.T) {
	t.Parallel()

	for _, version := range []int{0, toolregistry.WireProtocolVersion + 1} {
		_, err := (&Service{}).CallTool(context.Background(), &genregistry.CallToolPayload{
			WireProtocolVersion: version,
		})
		require.Error(t, err)

		var serviceErr *goa.ServiceError
		require.ErrorAs(t, err, &serviceErr)
		assert.Equal(t, "validation_error", serviceErr.Name)
	}
}

func TestRetryToolRejectsMismatchedWireProtocolBeforeDependencies(t *testing.T) {
	t.Parallel()

	for _, version := range []int{0, toolregistry.WireProtocolVersion + 1} {
		_, err := (&Service{}).RetryTool(context.Background(), &genregistry.RetryToolPayload{
			WireProtocolVersion: version,
		})
		require.Error(t, err)

		var serviceErr *goa.ServiceError
		require.ErrorAs(t, err, &serviceErr)
		assert.Equal(t, "validation_error", serviceErr.Name)
	}
}

func TestCallToolEnsuresAdmissionWithActiveRegistrationToken(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	catalog := newToolsetCatalog(
		newTestCatalogMap(),
		newTestTimeSource(time.Unix(1_700_000_000, 0)),
	)
	toolset := &genregistry.Toolset{
		Name: "test.toolset",
		Tools: []*genregistry.ToolSchema{{
			Name:          "lookup",
			PayloadSchema: []byte(`{"type":"object"}`),
			ResultSchema:  []byte(`{"type":"object"}`),
		}},
	}
	registration, err := catalog.Register(
		ctx,
		toolset,
		testAdmissionRevisionA,
		"provider-a",
		testIncarnationA,
		time.Hour,
	)
	require.NoError(t, err)

	resultStream := mockpulse.NewStream(t)
	resultStream.SetAdd(func(context.Context, string, []byte) (string, error) {
		return "1-0", nil
	})
	streamOpened := false
	resultStream.SetOpen(func(context.Context) error {
		streamOpened = true
		return nil
	})
	pulseClient := mockpulse.NewClient(t)
	pulseClient.SetStream(func(string, ...streamopts.Stream) (clientspulse.Stream, error) {
		return resultStream, nil
	})
	oldToken := strings.Repeat("b", 64)
	admissions := &recordingCallAdmissions{attached: &callAdmission{
		executionDeadline: time.Now().Add(toolregistry.MaxToolCallWait),
		registrationToken: oldToken,
	}}
	streams := &unitStreamManager{publishErrors: []error{errRoutingUnavailable}}
	svc := &Service{
		catalog:               catalog,
		validator:             newSchemaValidator(),
		streamManager:         streams,
		healthTracker:         unitHealthTracker{},
		callAdmissions:        admissions,
		pulseClient:           pulseClient,
		executionTimeout:      toolregistry.MaxToolCallWait,
		resultStreamTTL:       toolregistry.DefaultResultStreamTTL,
		providerLeaseDuration: DefaultProviderLeaseDuration,
	}

	result, err := svc.CallTool(ctx, &genregistry.CallToolPayload{
		Toolset:             toolset.Name,
		Tool:                "lookup",
		PayloadJSON:         []byte(`{"query":"status"}`),
		WireProtocolVersion: toolregistry.WireProtocolVersion,
		Meta: &genregistry.ToolCallMeta{
			RunID:      "run-1",
			SessionID:  "session-1",
			ToolCallID: "call-1",
		},
	})
	require.NoError(t, err)
	assert.True(t, streamOpened)
	assert.NotEqual(t, oldToken, registration.RegistrationToken)
	assert.Equal(t, 2, streams.publications)
	assert.Equal(t, 1, admissions.initializations)
	assert.Equal(t, registration.RegistrationToken, admissions.registrationToken)
	assert.Equal(t, registration.RegistrationToken, result.RegistrationToken)
	assert.Equal(t, registration.RegistrationToken, streams.message.RegistrationToken)
}

func TestCallToolRejectsUnpublishedCallWithoutProvider(t *testing.T) {
	t.Parallel()

	admission := &callAdmission{
		registrationToken: strings.Repeat("a", 64),
	}
	svc := &Service{
		catalog: newToolsetCatalog(
			newTestCatalogMap(),
			newTestTimeSource(time.Unix(1_700_000_000, 0)),
		),
		callAdmissions: &recordingCallAdmissions{attached: admission},
	}

	_, err := svc.CallTool(context.Background(), &genregistry.CallToolPayload{
		Toolset:             "missing.toolset",
		Tool:                "lookup",
		PayloadJSON:         []byte(`{}`),
		WireProtocolVersion: toolregistry.WireProtocolVersion,
		Meta: &genregistry.ToolCallMeta{
			RunID:      "run-1",
			SessionID:  "session-1",
			ToolCallID: "call-1",
		},
	})

	var serviceErr *goa.ServiceError
	require.ErrorAs(t, err, &serviceErr)
	assert.Equal(t, "call_not_admitted", serviceErr.Name)
}

func TestPrepareToolCallIdentityRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	_, err := prepareToolCallIdentity(
		"test.toolset",
		"lookup",
		[]byte(`{`),
		&genregistry.ToolCallMeta{
			RunID:      "run-1",
			SessionID:  "session-1",
			ToolCallID: "call-1",
		},
	)

	var serviceErr *goa.ServiceError
	require.ErrorAs(t, err, &serviceErr)
	assert.Equal(t, "validation_error", serviceErr.Name)
}

func TestCallAdmissionParseResultPreservesRegistrationToken(t *testing.T) {
	t.Parallel()

	const token = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := &callAdmissionStore{catalogHashKey: "registry:test:toolsets"}
	admission, err := store.parseResult(
		"test.toolset",
		"registry:test:call",
		"request-digest",
		"tool-use-1",
		[]any{
			int64(1),
			"request-digest",
			"1700000060000",
			"1700000120000",
			"0",
			"",
			token,
			"0",
			"",
			"0",
			string(outcomeUnknownPayload(token, "tool-use-1")),
			"",
			"",
			"",
		},
	)
	require.NoError(t, err)
	assert.Equal(t, token, admission.registrationToken)
}

func TestCallAdmissionParseResultRejectsMalformedState(t *testing.T) {
	t.Parallel()

	const token = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	base := []any{
		int64(1),
		"request-digest",
		"1700000060000",
		"1700000120000",
		"0",
		"",
		token,
		"0",
		"",
		"0",
		string(outcomeUnknownPayload(token, "tool-use-1")),
		"",
		"",
		"",
	}
	tests := []struct {
		name    string
		index   int
		value   any
		wantErr string
	}{
		{name: "deadline order", index: 3, value: "1700000060000", wantErr: "expiration does not follow"},
		{name: "noncanonical deadline", index: 2, value: "01700000060000", wantErr: "invalid execution deadline"},
		{name: "terminal state", index: 4, value: "unknown", wantErr: "invalid terminal"},
		{name: "terminal event", index: 5, value: "1-0", wantErr: "inconsistent terminal"},
		{name: "published state", index: 7, value: "unknown", wantErr: "invalid published"},
		{name: "orphan retry delay", index: 9, value: "100", wantErr: "inconsistent overload"},
		{name: "invalid outcome contract", index: 10, value: `{}`, wantErr: "outcome unknown payload"},
	}
	store := &callAdmissionStore{catalogHashKey: "registry:test:toolsets"}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := append([]any(nil), base...)
			value[test.index] = test.value

			_, err := store.parseResult(
				"test.toolset",
				"registry:test:call",
				"request-digest",
				"tool-use-1",
				value,
			)

			require.ErrorContains(t, err, test.wantErr)
		})
	}

	terminalPayload := outcomeUnknownPayload(token, "tool-use-1")
	terminalDigest := sha256.Sum256(terminalPayload)
	terminalBase := append([]any(nil), base...)
	terminalBase[4] = "1"
	terminalBase[5] = "1-0"
	terminalBase[7] = "1"
	terminalBase[11] = hex.EncodeToString(terminalDigest[:])
	terminalBase[12] = string(terminalPayload)
	terminalBase[13] = "provider"
	_, err := store.parseResult(
		"test.toolset",
		"registry:test:call",
		"request-digest",
		"tool-use-1",
		terminalBase,
	)
	require.NoError(t, err)

	for _, test := range []struct {
		name    string
		index   int
		value   any
		wantErr string
	}{
		{name: "terminal cause", index: 13, value: "unknown", wantErr: "invalid terminal cause"},
		{name: "terminal digest", index: 11, value: strings.Repeat("0", 64), wantErr: "digest does not match"},
		{name: "terminal payload", index: 12, value: `{}`, wantErr: "terminal payload"},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := append([]any(nil), terminalBase...)
			value[test.index] = test.value

			_, err := store.parseResult(
				"test.toolset",
				"registry:test:call",
				"request-digest",
				"tool-use-1",
				value,
			)

			require.ErrorContains(t, err, test.wantErr)
		})
	}

	uncertainBase := append([]any(nil), base...)
	uncertainBase[4] = "1"
	uncertainBase[5] = "2-0"
	uncertainBase[7] = "1"
	uncertainBase[11] = redisTerminalDigest(terminalPayload)
	uncertainBase[12] = string(terminalPayload)
	uncertainBase[13] = "execution_deadline"
	_, err = store.parseResult(
		"test.toolset",
		"registry:test:call",
		"request-digest",
		"tool-use-1",
		uncertainBase,
	)
	require.NoError(t, err)

	uncertainBase[11] = strings.Repeat("0", 40)
	_, err = store.parseResult(
		"test.toolset",
		"registry:test:call",
		"request-digest",
		"tool-use-1",
		uncertainBase,
	)
	require.ErrorContains(t, err, "digest does not match")
}

func TestToolUseIDForCallUsesRequiredRunScopedIdentity(t *testing.T) {
	t.Parallel()

	callID := "model-call-1"
	meta := &genregistry.ToolCallMeta{
		RunID:      "run-1",
		SessionID:  "session-1",
		ToolCallID: callID,
	}
	assert.Equal(
		t,
		toolregistry.DeriveToolUseID(meta.RunID, callID),
		toolUseIDForCall(meta),
	)
	otherRun := *meta
	otherRun.RunID = "run-2"
	assert.NotEqual(t, toolUseIDForCall(meta), toolUseIDForCall(&otherRun))
}

func TestPublishToolOutputDeltaRejectsByteOversizeBeforeDependencies(t *testing.T) {
	t.Parallel()

	err := (&Service{}).PublishToolOutputDelta(
		context.Background(),
		&genregistry.PublishToolOutputDeltaPayload{
			Delta: strings.Repeat("é", toolregistry.MaxToolOutputDeltaBytes/2+1),
		},
	)
	require.Error(t, err)
	var serviceErr *goa.ServiceError
	require.ErrorAs(t, err, &serviceErr)
	assert.Equal(t, "validation_error", serviceErr.Name)
}

func (r *recordingCallAdmissions) Ensure(
	_ context.Context,
	toolset, toolUseID, registrationToken, digest string,
	executionTimeout, ttl time.Duration,
	_ []byte,
) (callAdmission, bool, error) {
	r.registrationToken = registrationToken
	return callAdmission{
		key:               "unit:" + toolUseID,
		digest:            digest,
		executionDeadline: time.Now().Add(executionTimeout),
		expiresAt:         time.Now().Add(ttl),
		catalogField:      toolsetCatalogKey(toolset),
		registrationToken: registrationToken,
	}, true, nil
}

func (r *recordingCallAdmissions) Reject(
	_ context.Context,
	_, _, _ string,
	rejection callRejection,
	_ time.Duration,
) (callAdmission, error) {
	return callAdmission{}, &callRejectedError{rejection: rejection}
}

func (r *recordingCallAdmissions) Attach(context.Context, string, string, string) (callAdmission, error) {
	if r.attached != nil {
		return *r.attached, nil
	}
	return callAdmission{}, errCallAdmissionNotFound
}

func (r *recordingCallAdmissions) InitializeResultStream(context.Context, callAdmission, string) error {
	r.initializations++
	return nil
}

func (r *recordingCallAdmissions) RestoreTerminal(context.Context, callAdmission, string) error {
	return nil
}

func (r *recordingCallAdmissions) SettleLostClaimsForLease(context.Context, string, string) error {
	return nil
}

func (r *recordingCallAdmissions) Complete(
	context.Context,
	string, string, string, string, string, string, string,
	[]byte,
) error {
	return nil
}

func (r *recordingCallAdmissions) PublishLiveEvent(
	context.Context,
	string, string, string, string, string, string, string, string,
	[]byte,
) error {
	return nil
}

func (r *recordingCallAdmissions) ReportOverload(
	context.Context,
	string, string, string, string, string, string, string,
	[]byte, []byte,
) error {
	return nil
}

func (r *recordingCallAdmissions) Claim(
	context.Context,
	string, string, string, string, string, string, string,
	[]byte,
) (callClaimDisposition, error) {
	return callClaimExecute, nil
}

func (m *unitStreamManager) GetOrCreateStream(context.Context, string) (clientspulse.Stream, string, error) {
	return nil, "", nil
}

func (m *unitStreamManager) PublishToolCall(context.Context, string, toolregistry.ToolCallMessage) error {
	return nil
}

func (m *unitStreamManager) PublishAdmittedToolCall(
	_ context.Context,
	_ string,
	message toolregistry.ToolCallMessage,
	_ callAdmission,
	_ string,
) error {
	m.publications++
	m.message = message
	if m.publications <= len(m.publishErrors) {
		return m.publishErrors[m.publications-1]
	}
	return nil
}

func (unitHealthTracker) Health(context.Context, string, string) (ToolsetHealth, error) {
	return ToolsetHealth{Healthy: true}, nil
}

func (unitHealthTracker) RecordPong(context.Context, string, string, string, string) error {
	return nil
}

func (unitHealthTracker) EnsurePingLoop(context.Context, string) error {
	return nil
}

func (unitHealthTracker) Close() error {
	return nil
}
