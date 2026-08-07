package registry

import (
	"context"
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
	// recordingCallAdmissions captures the immutable token selected by CallTool.
	recordingCallAdmissions struct {
		registrationToken string
	}

	// unitStreamManager records admitted publication without Redis.
	unitStreamManager struct {
		message toolregistry.ToolCallMessage
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
	admissions := &recordingCallAdmissions{}
	streams := &unitStreamManager{}
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
	assert.Equal(t, registration.RegistrationToken, admissions.registrationToken)
	assert.Equal(t, registration.RegistrationToken, result.RegistrationToken)
	assert.Equal(t, registration.RegistrationToken, streams.message.RegistrationToken)
}

func TestCallAdmissionParseResultPreservesRegistrationToken(t *testing.T) {
	t.Parallel()

	const token = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := &callAdmissionStore{catalogHashKey: "registry:test:toolsets"}
	admission, err := store.parseResult(
		"test.toolset",
		"registry:test:call",
		"request-digest",
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
		},
	)
	require.NoError(t, err)
	assert.Equal(t, token, admission.registrationToken)
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

func (r *recordingCallAdmissions) Attach(context.Context, string, string, string) (callAdmission, error) {
	return callAdmission{}, errCallAdmissionNotFound
}

func (r *recordingCallAdmissions) InitializeResultStream(context.Context, callAdmission, string) error {
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
	m.message = message
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
