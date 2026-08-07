//go:build integration

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goa "goa.design/goa/v3/pkg"
	streamopts "goa.design/pulse/streaming/options"

	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
)

// TestRegistrationIdempotence verifies identical-schema lease renewal.
// **Feature: internal-tool-registry, Property 2: Registration idempotence**
// *For any* toolset, a second provider registering the identical schema joins
// the same stable catalog generation.
// **Validates: Requirements 2.2, 2.5**
func TestRegistrationIdempotence(t *testing.T) {
	rdb := getRedis(t)
	pulseClient, err := clientspulse.New(clientspulse.Options{Redis: rdb})
	if err != nil {
		t.Fatalf("create pulse client: %v", err)
	}

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("identical registration preserves catalog generation", prop.ForAll(
		func(tc registrationIdempotenceTestCase) bool {
			ctx := context.Background()

			// Create mock dependencies.
			mockSM := newMockStreamManagerForService()
			mockHT := newMockHealthTracker()

			// Create the service.
			svc, err := newTestServiceForServiceTests(pulseClient, mockSM, mockHT)
			if err != nil {
				return false
			}

			// First registration.
			result1, err := svc.Register(ctx, tc.firstPayload)
			if err != nil {
				return false
			}
			if result1.RegisteredAt == "" {
				return false
			}

			// Second registration from another provider.
			result2, err := svc.Register(ctx, tc.secondPayload)
			if err != nil {
				return false
			}
			if result2.RegisteredAt == "" {
				return false
			}

			// Retrieve the toolset and verify the original generation is stable.
			retrieved, err := svc.GetToolset(ctx, &genregistry.GetToolsetPayload{
				Name: tc.firstPayload.Name,
			})
			if err != nil {
				return false
			}

			if !stringPtrEqualForService(retrieved.Description, tc.firstPayload.Description) {
				return false
			}
			if !stringPtrEqualForService(retrieved.Version, tc.firstPayload.Version) {
				return false
			}
			if !stringSliceEqualForService(retrieved.Tags, tc.firstPayload.Tags) {
				return false
			}
			if len(retrieved.Tools) != len(tc.firstPayload.Tools) {
				return false
			}
			if result1.RegisteredAt != result2.RegisteredAt ||
				retrieved.RegisteredAt != result1.RegisteredAt {
				return false
			}
			if result1.RegistrationToken != result2.RegistrationToken {
				return false
			}
			if len(mockHT.startedToolsets) != 2 {
				return false
			}

			return true
		},
		genRegistrationIdempotenceTestCase(),
	))

	properties.TestingRun(t)
}

func newTestServiceForServiceTests(pulseClient clientspulse.Client, streamManager StreamManager, healthTracker HealthTracker, seed ...*genregistry.Toolset) (*Service, error) {
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	catalog := newToolsetCatalog(newTestCatalogMap(), clock)
	ctx := context.Background()
	for _, toolset := range seed {
		if err := saveTestToolset(ctx, catalog, toolset); err != nil {
			return nil, err
		}
	}
	return newService(serviceOptions{
		catalog:        catalog,
		StreamManager:  streamManager,
		HealthTracker:  healthTracker,
		CallAdmissions: newCallAdmissionStore(testRedisClient, "service-tests"),
		PulseClient:    pulseClient,
	})
}

func saveTestToolset(ctx context.Context, catalog *toolsetCatalog, toolset *genregistry.Toolset) error {
	_, err := catalog.Register(
		ctx,
		toolset,
		testAdmissionRevisionA,
		"test-provider",
		testIncarnationA,
		24*time.Hour,
	)
	return err
}

// registrationIdempotenceTestCase represents a test case for registration idempotence.
type registrationIdempotenceTestCase struct {
	firstPayload  *genregistry.RegisterPayload
	secondPayload *genregistry.RegisterPayload
}

// genRegistrationIdempotenceTestCase generates test cases for registration idempotence.
func genRegistrationIdempotenceTestCase() gopter.Gen {
	return genToolsetNameForIdempotence().FlatMap(func(name any) gopter.Gen {
		toolsetName := name.(string)
		return genRegisterPayload(toolsetName).Map(func(first *genregistry.RegisterPayload) registrationIdempotenceTestCase {
			second := *first
			second.ProviderID = toolsetName + "/provider-b"
			second.ProviderIncarnationID = testIncarnationB
			return registrationIdempotenceTestCase{
				firstPayload:  first,
				secondPayload: &second,
			}
		})
	}, reflect.TypeOf(registrationIdempotenceTestCase{}))
}

// genToolsetNameForIdempotence generates unique toolset names for idempotence tests.
func genToolsetNameForIdempotence() gopter.Gen {
	return gen.Identifier().Map(func(s string) string {
		return "idempotence-test-" + s
	})
}

// genRegisterPayload generates a RegisterPayload with the given toolset name.
func genRegisterPayload(name string) gopter.Gen {
	return gopter.CombineGens(
		genOptionalStringForService(),
		genOptionalStringForService(),
		genTagsForService(),
		genToolSchemaSlice(),
	).Map(func(vals []any) *genregistry.RegisterPayload {
		var (
			desc    *string
			version *genregistry.SemVer
		)
		if vals[0] != nil {
			desc = vals[0].(*string)
		}
		if vals[1] != nil {
			raw := vals[1].(*string)
			v := genregistry.SemVer(*raw)
			version = &v
		}
		return &genregistry.RegisterPayload{
			Name:                  name,
			Description:           desc,
			Version:               version,
			Tags:                  vals[2].([]string),
			Tools:                 vals[3].([]*genregistry.ToolSchema),
			ProviderID:            name + "/provider-a",
			ProviderIncarnationID: testIncarnationA,
			AdmissionRevision:     testAdmissionRevisionA,
			WireProtocolVersion:   toolregistry.WireProtocolVersion,
		}
	})
}

// genOptionalStringForService generates an optional string pointer.
func genOptionalStringForService() gopter.Gen {
	return gen.PtrOf(gen.OneConstOf(
		"A description",
		"Another description",
		"Tools for processing",
		"Service utilities",
		"Updated description",
		"New version info",
	))
}

// genTagsForService generates a slice of tags.
func genTagsForService() gopter.Gen {
	return gen.SliceOfN(3, gen.OneConstOf(
		"data",
		"etl",
		"analytics",
		"search",
		"notification",
		"api",
	))
}

// genToolSchemaSlice generates a slice of ToolSchema for registration.
func genToolSchemaSlice() gopter.Gen {
	return gen.SliceOfN(3, genToolSchema()).SuchThat(func(tools []*genregistry.ToolSchema) bool {
		return len(tools) > 0 // Ensure at least one tool
	})
}

// genToolSchema generates a single ToolSchema.
func genToolSchema() gopter.Gen {
	return gopter.CombineGens(
		genToolNameForService(),
		genOptionalStringForService(),
		genSchemaForService(),
		genSchemaForService(),
	).Map(func(vals []any) *genregistry.ToolSchema {
		var desc *string
		if vals[1] != nil {
			desc = vals[1].(*string)
		}
		return &genregistry.ToolSchema{
			Name:          vals[0].(string),
			Description:   desc,
			PayloadSchema: vals[2].([]byte),
			ResultSchema:  vals[3].([]byte),
		}
	})
}

// genToolNameForService generates valid tool names.
func genToolNameForService() gopter.Gen {
	return gen.OneConstOf(
		"analyze",
		"transform",
		"query",
		"notify",
		"search",
	)
}

// genSchemaForService generates JSON schema bytes.
func genSchemaForService() gopter.Gen {
	return gen.OneConstOf(
		[]byte(`{"type":"object"}`),
		[]byte(`{"type":"string"}`),
		[]byte(`{"type":"array","items":{"type":"string"}}`),
	)
}

// stringPtrEqualForService checks if two string pointers are equal.
func stringPtrEqualForService[T ~string](a, b *T) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// stringSliceEqualForService checks if two string slices are equal.
func stringSliceEqualForService(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCallToolPayloadValidation verifies Property 8: CallTool payload validation.
// **Feature: internal-tool-registry, Property 8: CallTool payload validation**
// *For any* tool call with a payload that does not conform to the tool's input schema,
// CallTool should reject with a validation error.
// **Validates: Requirements 9.2**
func TestCallToolPayloadValidation(t *testing.T) {
	rdb := getRedis(t)
	pulseClient, err := clientspulse.New(clientspulse.Options{Redis: rdb})
	if err != nil {
		t.Fatalf("create pulse client: %v", err)
	}

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("CallTool rejects payloads that don't match input schema", prop.ForAll(
		func(tc payloadValidationTestCase) bool {
			ctx := context.Background()

			// Create mock dependencies.
			mockSM := newMockStreamManagerForService()
			mockHT := newMockHealthTracker() // Always healthy

			// Create the service.
			svc, err := newTestServiceForServiceTests(pulseClient, mockSM, mockHT, tc.toolset)
			if err != nil {
				return false
			}

			// Call the tool with an invalid payload.
			_, err = svc.CallTool(ctx, &genregistry.CallToolPayload{
				Toolset:             tc.toolset.Name,
				Tool:                tc.toolName,
				PayloadJSON:         tc.invalidPayload,
				WireProtocolVersion: toolregistry.WireProtocolVersion,
				Meta: &genregistry.ToolCallMeta{
					RunID:     "test-run",
					SessionID: "test-session",
				},
			})

			// Should return a validation error.
			if err == nil {
				return false
			}

			// Check that it's a validation error.
			var svcErr *goa.ServiceError
			if !errors.As(err, &svcErr) {
				return false
			}
			return svcErr.Name == "validation_error"
		},
		genPayloadValidationTestCase(),
	))

	properties.TestingRun(t)
}

func TestCallToolRejectsToolsetWithoutPayloadSchema(t *testing.T) {
	ctx := context.Background()
	pulseClient := mockpulse.NewClient(t)
	resultStream := mockpulse.NewStream(t)
	pulseClient.AddStream(func(name string, _ ...streamopts.Stream) (clientspulse.Stream, error) {
		return resultStream, nil
	})
	resultStream.AddAdd(func(ctx context.Context, event string, payload []byte) (string, error) {
		return "1-0", nil
	})

	toolset := &genregistry.Toolset{
		Name: "toolset-1",
		Tools: []*genregistry.ToolSchema{
			{
				Name:         "lookup",
				ResultSchema: []byte(`{"type":"object"}`),
			},
		},
		RegisteredAt: "2024-01-15T10:30:00Z",
	}

	svc, err := newTestServiceForServiceTests(pulseClient, newMockStreamManagerForService(), newMockHealthTracker(), toolset)
	require.NoError(t, err)

	_, err = svc.CallTool(ctx, &genregistry.CallToolPayload{
		Toolset:             "toolset-1",
		Tool:                "lookup",
		PayloadJSON:         []byte(`{"query":"ok"}`),
		WireProtocolVersion: toolregistry.WireProtocolVersion,
		Meta: &genregistry.ToolCallMeta{
			RunID:     "run-1",
			SessionID: "session-1",
		},
	})
	require.Error(t, err)

	var svcErr *goa.ServiceError
	require.ErrorAs(t, err, &svcErr)
	require.Equal(t, "validation_error", svcErr.Name)
}

func TestCallToolDerivesGlobalTransportIdentity(t *testing.T) {
	ctx := context.Background()
	pulseClient := mockpulse.NewClient(t)
	resultStream := mockpulse.NewStream(t)
	openCount := 0
	resultStream.SetOpen(func(context.Context) error {
		openCount++
		return nil
	})
	for range 3 {
		pulseClient.AddStream(func(name string, opts ...streamopts.Stream) (clientspulse.Stream, error) {
			retention := streamopts.ParseStreamOptions(opts...)
			assert.True(t, retention.DeadlineSet)
			assert.True(t, retention.MaxLenSet)
			assert.Equal(t, toolregistry.ResultStreamMaxLen, retention.MaxLen)
			assert.False(t, retention.Unbounded)
			return resultStream, nil
		})
	}
	for range 2 {
		resultStream.AddAdd(func(context.Context, string, []byte) (string, error) {
			return "1-0", nil
		})
	}

	toolset := &genregistry.Toolset{
		Name: "toolset-1",
		Tools: []*genregistry.ToolSchema{
			{
				Name:          "lookup",
				PayloadSchema: []byte(`{"type":"object"}`),
				ResultSchema:  []byte(`{"type":"object"}`),
			},
		},
		RegisteredAt: "2024-01-15T10:30:00Z",
	}

	streams := newMockStreamManagerForService()
	svc, err := newTestServiceForServiceTests(pulseClient, streams, newMockHealthTracker(), toolset)
	require.NoError(t, err)

	toolCallID := "tool-call-1"
	payload := &genregistry.CallToolPayload{
		Toolset:             "toolset-1",
		Tool:                "lookup",
		PayloadJSON:         []byte(`{"query":"ok"}`),
		WireProtocolVersion: toolregistry.WireProtocolVersion,
		Meta: &genregistry.ToolCallMeta{
			RunID:            "run-1",
			SessionID:        "session-1",
			ToolCallID:       toolCallID,
			TurnID:           nil,
			ParentToolCallID: nil,
		},
	}

	first, err := svc.CallTool(ctx, payload)
	require.NoError(t, err)
	second, err := svc.CallTool(ctx, payload)
	require.NoError(t, err)
	otherRun := *payload
	otherRunMeta := *payload.Meta
	otherRunMeta.RunID = "run-2"
	otherRun.Meta = &otherRunMeta
	third, err := svc.CallTool(ctx, &otherRun)
	require.NoError(t, err)

	expected := toolregistry.DeriveToolUseID("run-1", toolCallID)
	require.Equal(t, expected, first.ToolUseID)
	require.Equal(t, expected, second.ToolUseID)
	require.NotEqual(t, expected, third.ToolUseID)
	require.NotEmpty(t, first.RegistrationToken)
	require.Equal(t, first.RegistrationToken, second.RegistrationToken)
	require.NotEmpty(t, first.ExecutionDeadline)
	require.NotEmpty(t, first.ResultStreamExpiresAt)
	require.Len(t, streams.messages["toolset-1"], 2)
	require.Equal(t, expected, streams.messages["toolset-1"][0].ToolUseID)
	require.Equal(t, third.ToolUseID, streams.messages["toolset-1"][1].ToolUseID)
	expiresAt, err := time.Parse(time.RFC3339Nano, first.ResultStreamExpiresAt)
	require.NoError(t, err)
	executionDeadline, err := time.Parse(time.RFC3339Nano, first.ExecutionDeadline)
	require.NoError(t, err)
	require.True(t, executionDeadline.Before(expiresAt))
	require.Equal(t, executionDeadline.UnixMilli(), streams.messages["toolset-1"][0].ExecutionDeadlineUnixMilli)
	require.Equal(t, expiresAt.UnixMilli(), streams.messages["toolset-1"][0].ResultStreamExpiresAtUnixMilli)
	require.NotEmpty(t, streams.messages["toolset-1"][0].RegistrationToken)
	require.Equal(t, 3, openCount)
	require.Equal(
		t,
		streams.messages["toolset-1"][0].RegistrationToken,
		first.RegistrationToken,
	)
}

func TestRetryToolRejectsAdmissionRolloverBeforePublication(t *testing.T) {
	ctx := context.Background()
	pulseClient := mockpulse.NewClient(t)
	resultStream := mockpulse.NewStream(t)
	resultStream.SetOpen(func(context.Context) error {
		return nil
	})
	pulseClient.AddStream(func(string, ...streamopts.Stream) (clientspulse.Stream, error) {
		return resultStream, nil
	})
	resultStream.AddAdd(func(context.Context, string, []byte) (string, error) {
		return "1-0", nil
	})
	toolset := &genregistry.Toolset{
		Name: "toolset-1",
		Tools: []*genregistry.ToolSchema{{
			Name:          "lookup",
			PayloadSchema: []byte(`{"type":"object"}`),
			ResultSchema:  []byte(`{"type":"object"}`),
		}},
		RegisteredAt: "2024-01-15T10:30:00Z",
	}
	streams := newMockStreamManagerForService()
	svc, err := newTestServiceForServiceTests(
		pulseClient,
		streams,
		newMockHealthTracker(),
		toolset,
	)
	require.NoError(t, err)
	call := &genregistry.CallToolPayload{
		Toolset:             toolset.Name,
		Tool:                "lookup",
		PayloadJSON:         []byte(`{"query":"ok"}`),
		WireProtocolVersion: toolregistry.WireProtocolVersion,
		Meta: &genregistry.ToolCallMeta{
			RunID:      "run-1",
			SessionID:  "session-1",
			ToolCallID: "tool-call-1",
		},
	}
	admitted, err := svc.CallTool(ctx, call)
	require.NoError(t, err)
	require.Len(t, streams.messages[toolset.Name], 1)

	require.NoError(t, svc.catalog.ReleaseProvider(
		ctx,
		toolset.Name,
		"test-provider",
		testIncarnationA,
		admitted.RegistrationToken,
	))
	replacement, err := svc.catalog.Register(
		ctx,
		toolset,
		testAdmissionRevisionB,
		"replacement-provider",
		testIncarnationB,
		24*time.Hour,
	)
	require.NoError(t, err)
	require.NotEqual(t, admitted.RegistrationToken, replacement.RegistrationToken)

	_, err = svc.RetryTool(ctx, &genregistry.RetryToolPayload{
		Toolset:                   call.Toolset,
		Tool:                      call.Tool,
		PayloadJSON:               call.PayloadJSON,
		Meta:                      call.Meta,
		WireProtocolVersion:       toolregistry.WireProtocolVersion,
		ExpectedRegistrationToken: admitted.RegistrationToken,
	})
	require.Error(t, err)
	var serviceErr *goa.ServiceError
	require.ErrorAs(t, err, &serviceErr)
	assert.Equal(t, "admission_conflict", serviceErr.Name)
	assert.Len(t, streams.messages[toolset.Name], 1)
}

func TestCallToolMapsHealthInfrastructureFailure(t *testing.T) {
	t.Parallel()

	toolset := &genregistry.Toolset{
		Name: "toolset-1",
		Tools: []*genregistry.ToolSchema{{
			Name:          "lookup",
			PayloadSchema: []byte(`{"type":"object"}`),
			ResultSchema:  []byte(`{"type":"object"}`),
		}},
		RegisteredAt: "2024-01-15T10:30:00Z",
	}
	health := newMockHealthTracker()
	health.healthErr = errors.New("Redis unavailable")
	svc, err := newTestServiceForServiceTests(
		mockpulse.NewClient(t),
		newMockStreamManagerForService(),
		health,
		toolset,
	)
	require.NoError(t, err)

	_, err = svc.CallTool(context.Background(), &genregistry.CallToolPayload{
		Toolset:             "toolset-1",
		Tool:                "lookup",
		PayloadJSON:         []byte(`{}`),
		WireProtocolVersion: toolregistry.WireProtocolVersion,
		Meta: &genregistry.ToolCallMeta{
			RunID:     "run-1",
			SessionID: "session-1",
		},
	})
	var serviceErr *goa.ServiceError
	require.ErrorAs(t, err, &serviceErr)
	assert.Equal(t, "service_unavailable", serviceErr.Name)
}

func TestNewServiceRejectsUnsafeRetentionAndLeaseDurations(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		resultTTL     time.Duration
		providerLease time.Duration
	}{
		{
			name:          "result TTL below call wait budget",
			resultTTL:     toolregistry.MinResultStreamTTL - time.Millisecond,
			providerLease: DefaultProviderLeaseDuration,
		},
		{
			name:          "result TTL above orphan bound",
			resultTTL:     toolregistry.MaxResultStreamTTL + time.Millisecond,
			providerLease: DefaultProviderLeaseDuration,
		},
		{
			name:          "provider lease below shutdown retry budget",
			resultTTL:     toolregistry.DefaultResultStreamTTL,
			providerLease: toolregistry.MinProviderLeaseDuration - time.Millisecond,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := newService(serviceOptions{
				catalog:               newToolsetCatalog(newTestCatalogMap(), newTestTimeSource(time.Now())),
				StreamManager:         newMockStreamManagerForService(),
				HealthTracker:         newMockHealthTracker(),
				PulseClient:           mockpulse.NewClient(t),
				ResultStreamTTL:       test.resultTTL,
				ProviderLeaseDuration: test.providerLease,
			})
			require.Error(t, err)
		})
	}
}

// payloadValidationTestCase represents a test case for payload validation.
type payloadValidationTestCase struct {
	toolset        *genregistry.Toolset
	toolName       string
	invalidPayload json.RawMessage
}

// genPayloadValidationTestCase generates test cases with toolsets and invalid payloads.
func genPayloadValidationTestCase() gopter.Gen {
	return gopter.CombineGens(
		genSchemaType(),
		genToolsetNameForValidation(),
	).FlatMap(func(vals any) gopter.Gen {
		arr := vals.([]any)
		schemaType := arr[0].(string)
		toolsetName := arr[1].(string)

		return genInvalidPayloadForSchema(schemaType).Map(func(invalidPayload json.RawMessage) payloadValidationTestCase {
			// Create a tool with the schema.
			schema := schemaForType(schemaType)
			toolName := "test-tool"
			desc := "A test tool"

			tool := &genregistry.ToolSchema{
				Name:          toolName,
				Description:   &desc,
				PayloadSchema: schema,
			}

			// Create the toolset.
			toolset := &genregistry.Toolset{
				Name:         toolsetName,
				Tools:        []*genregistry.ToolSchema{tool},
				RegisteredAt: "2024-01-15T10:30:00Z",
			}

			return payloadValidationTestCase{
				toolset:        toolset,
				toolName:       toolName,
				invalidPayload: invalidPayload,
			}
		})
	}, reflect.TypeOf(payloadValidationTestCase{}))
}

// genSchemaType generates schema types for testing.
func genSchemaType() gopter.Gen {
	return gen.OneConstOf(
		"object-required",
		"string",
		"integer",
		"array-of-strings",
	)
}

// genToolsetNameForValidation generates unique toolset names.
func genToolsetNameForValidation() gopter.Gen {
	return gen.Identifier().Map(func(s string) string {
		return "validation-test-" + s
	})
}

// schemaForType returns a JSON Schema for the given type.
func schemaForType(schemaType string) []byte {
	switch schemaType {
	case "object-required":
		return []byte(`{"type":"object","properties":{"name":{"type":"string"},"count":{"type":"integer"}},"required":["name","count"]}`)
	case "string":
		return []byte(`{"type":"string"}`)
	case "integer":
		return []byte(`{"type":"integer"}`)
	case "array-of-strings":
		return []byte(`{"type":"array","items":{"type":"string"}}`)
	default:
		return []byte(`{"type":"object"}`)
	}
}

// genInvalidPayloadForSchema generates payloads that don't match the given schema type.
func genInvalidPayloadForSchema(schemaType string) gopter.Gen {
	switch schemaType {
	case "object-required":
		// Generate objects missing required fields or with wrong types.
		return gen.OneConstOf(
			json.RawMessage(`{"name":"test"}`),
			json.RawMessage(`{"count":42}`),
			json.RawMessage(`{}`),
			json.RawMessage(`{"name":123,"count":42}`),
			json.RawMessage(`{"name":"test","count":"string"}`),
		)
	case "string":
		// Generate non-string values.
		return gen.OneConstOf(
			json.RawMessage(`42`),
			json.RawMessage(`true`),
			json.RawMessage(`["array"]`),
			json.RawMessage(`{"key":"value"}`),
		)
	case "integer":
		// Generate non-integer values.
		return gen.OneConstOf(
			json.RawMessage(`"string"`),
			json.RawMessage(`true`),
			json.RawMessage(`[1,2,3]`),
			json.RawMessage(`{"key":"value"}`),
			json.RawMessage(`3.14`),
		)
	case "array-of-strings":
		// Generate non-arrays or arrays with wrong item types.
		return gen.OneConstOf(
			json.RawMessage(`"not-an-array"`),
			json.RawMessage(`42`),
			json.RawMessage(`[1,2,3]`),
			json.RawMessage(`{"key":"value"}`),
			json.RawMessage(`["string",42]`),
		)
	default:
		return gen.OneConstOf(json.RawMessage(`"invalid"`))
	}
}

// --- Mock implementations for service tests ---

// mockStreamManagerForService is a mock StreamManager for service tests.
type mockStreamManagerForService struct {
	mu              sync.RWMutex
	createdToolsets []string
	messages        map[string][]toolregistry.ToolCallMessage
	publications    map[string]struct{}
}

func newMockStreamManagerForService() *mockStreamManagerForService {
	return &mockStreamManagerForService{
		messages:     make(map[string][]toolregistry.ToolCallMessage),
		publications: make(map[string]struct{}),
	}
}

func (m *mockStreamManagerForService) GetOrCreateStream(ctx context.Context, toolset string) (clientspulse.Stream, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createdToolsets = append(m.createdToolsets, toolset)
	return nil, "mock-stream:" + toolset, nil
}

func (m *mockStreamManagerForService) GetStream(toolset string) clientspulse.Stream {
	return nil
}

func (m *mockStreamManagerForService) RemoveStream(toolset string) {}

func (m *mockStreamManagerForService) PublishToolCall(ctx context.Context, toolset string, msg toolregistry.ToolCallMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages[toolset] = append(m.messages[toolset], msg)
	return nil
}

func (m *mockStreamManagerForService) PublishAdmittedToolCall(
	ctx context.Context,
	toolset string,
	msg toolregistry.ToolCallMessage,
	admission callAdmission,
	overloadEventID string,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	publicationKey := admission.key + "\x00" + overloadEventID
	if _, published := m.publications[publicationKey]; published {
		return nil
	}
	m.publications[publicationKey] = struct{}{}
	m.messages[toolset] = append(m.messages[toolset], msg)
	return nil
}

var _ StreamManager = (*mockStreamManagerForService)(nil)

// mockHealthTracker is a mock HealthTracker for service tests.
type mockHealthTracker struct {
	healthy            bool
	startedToolsets    []string
	registrationTokens []string
	registerErr        error
	healthErr          error
}

func newMockHealthTracker() *mockHealthTracker {
	return &mockHealthTracker{healthy: true}
}

func (m *mockHealthTracker) RecordPong(
	ctx context.Context,
	toolset, providerID, incarnationID, pingID string,
) error {
	return nil
}

func (m *mockHealthTracker) RegisterProvider(
	ctx context.Context,
	toolset, providerID, registrationToken string,
	leaseDuration time.Duration,
) error {
	if m.registerErr != nil {
		return m.registerErr
	}
	m.registrationTokens = append(m.registrationTokens, registrationToken)
	return nil
}

func (m *mockHealthTracker) RemoveProvider(ctx context.Context, toolset, providerID, registrationToken string) error {
	return nil
}

func (m *mockHealthTracker) Health(ctx context.Context, toolset, registrationToken string) (ToolsetHealth, error) {
	if m.healthErr != nil {
		return ToolsetHealth{}, m.healthErr
	}
	return ToolsetHealth{Healthy: m.healthy}, nil
}

func (m *mockHealthTracker) RemoveGeneration(ctx context.Context, toolset, registrationToken string) error {
	return nil
}

func (m *mockHealthTracker) EnsurePingLoop(ctx context.Context, toolset string) error {
	m.startedToolsets = append(m.startedToolsets, toolset)
	return nil
}

func (m *mockHealthTracker) Close() error {
	return nil
}

var _ HealthTracker = (*mockHealthTracker)(nil)

// TestUnregisterRemovesFromListing verifies Property 4: Unregister removes from listing.
// **Feature: internal-tool-registry, Property 4: Unregister removes from listing**
// *For any* registered toolset, unregistering it should cause it to no longer
// appear in ListToolsets results.
// **Validates: Requirements 5.1, 6.1**
func TestUnregisterRemovesFromListing(t *testing.T) {
	rdb := getRedis(t)
	pulseClient, err := clientspulse.New(clientspulse.Options{Redis: rdb})
	if err != nil {
		t.Fatalf("create pulse client: %v", err)
	}

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("unregistered toolset does not appear in listing", prop.ForAll(
		func(tc unregisterRemovesFromListingTestCase) bool {
			ctx := context.Background()

			// Create mock dependencies.
			mockSM := newMockStreamManagerForService()
			mockHT := newMockHealthTracker()

			// Create the service.
			svc, err := newTestServiceForServiceTests(pulseClient, mockSM, mockHT)
			if err != nil {
				return false
			}

			// Register all toolsets.
			for _, payload := range tc.toolsets {
				_, err := svc.Register(ctx, payload)
				if err != nil {
					return false
				}
			}

			// Verify the target toolset appears in listing before unregister.
			listResult, err := svc.ListToolsets(ctx, &genregistry.ListToolsetsPayload{})
			if err != nil {
				return false
			}
			if !containsToolsetInfo(listResult.Toolsets, tc.targetName) {
				return false // Target should be in listing before unregister
			}
			token, err := svc.catalog.RegistrationToken(ctx, tc.targetName)
			if err != nil {
				return false
			}

			// Unregister the target toolset.
			err = svc.Unregister(ctx, &genregistry.UnregisterPayload{
				Name:                      tc.targetName,
				ExpectedRegistrationToken: token,
			})
			if err != nil {
				return false
			}

			// Verify the target toolset no longer appears in listing.
			listResult, err = svc.ListToolsets(ctx, &genregistry.ListToolsetsPayload{})
			if err != nil {
				return false
			}
			if containsToolsetInfo(listResult.Toolsets, tc.targetName) {
				return false // Target should NOT be in listing after unregister
			}

			// Verify other toolsets still appear in listing.
			for _, payload := range tc.toolsets {
				if payload.Name == tc.targetName {
					continue
				}
				if !containsToolsetInfo(listResult.Toolsets, payload.Name) {
					return false // Other toolsets should still be in listing
				}
			}

			return true
		},
		genUnregisterRemovesFromListingTestCase(),
	))

	properties.TestingRun(t)
}

// unregisterRemovesFromListingTestCase represents a test case for unregister removes from listing.
type unregisterRemovesFromListingTestCase struct {
	toolsets   []*genregistry.RegisterPayload
	targetName string
}

// genUnregisterRemovesFromListingTestCase generates test cases for unregister removes from listing.
func genUnregisterRemovesFromListingTestCase() gopter.Gen {
	return gopter.CombineGens(
		gen.IntRange(1, 5),
		gen.Identifier(),
	).FlatMap(func(vals any) gopter.Gen {
		arr := vals.([]any)
		count := arr[0].(int)
		baseName := arr[1].(string)

		// Generate unique toolset names.
		names := make([]string, count)
		for i := range count {
			names[i] = fmt.Sprintf("unregister-test-%s-%d", baseName, i)
		}

		// Generate payloads for each name.
		gens := make([]gopter.Gen, count)
		for i, name := range names {
			gens[i] = genRegisterPayload(name)
		}

		return gopter.CombineGens(gens...).FlatMap(func(payloadsAny any) gopter.Gen {
			payloadsArr := payloadsAny.([]any)
			toolsets := make([]*genregistry.RegisterPayload, len(payloadsArr))
			for i, p := range payloadsArr {
				toolsets[i] = p.(*genregistry.RegisterPayload)
			}

			// Pick a random target index.
			return gen.IntRange(0, len(toolsets)-1).Map(func(idx int) unregisterRemovesFromListingTestCase {
				return unregisterRemovesFromListingTestCase{
					toolsets:   toolsets,
					targetName: toolsets[idx].Name,
				}
			})
		}, reflect.TypeOf(unregisterRemovesFromListingTestCase{}))
	}, reflect.TypeOf(unregisterRemovesFromListingTestCase{}))
}

// containsToolsetInfo checks if a slice of ToolsetInfo contains a toolset with the given name.
func containsToolsetInfo(infos []*genregistry.ToolsetInfo, name string) bool {
	for _, info := range infos {
		if info.Name == name {
			return true
		}
	}
	return false
}

// TestInvalidSchemaRejection verifies Property 3: Invalid schema rejection.
// **Feature: internal-tool-registry, Property 3: Invalid schema rejection**
// *For any* toolset with malformed JSON Schema in tool definitions, registration
// should fail with a validation error.
// **Validates: Requirements 2.3**
func TestInvalidSchemaRejection(t *testing.T) {
	rdb := getRedis(t)
	pulseClient, err := clientspulse.New(clientspulse.Options{Redis: rdb})
	if err != nil {
		t.Fatalf("create pulse client: %v", err)
	}

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("registration rejects toolsets with invalid schemas", prop.ForAll(
		func(tc invalidSchemaTestCase) bool {
			ctx := context.Background()

			// Create mock dependencies.
			mockSM := newMockStreamManagerForService()
			mockHT := newMockHealthTracker()

			// Create the service.
			svc, err := newTestServiceForServiceTests(pulseClient, mockSM, mockHT)
			if err != nil {
				return false
			}

			// Attempt to register the toolset with invalid schema.
			_, err = svc.Register(ctx, tc.payload)

			// Should return a validation error.
			if err == nil {
				return false
			}

			// Check that it's a validation error.
			var svcErr *goa.ServiceError
			if !errors.As(err, &svcErr) {
				return false
			}
			return svcErr.Name == "validation_error"
		},
		genInvalidSchemaTestCase(),
	))

	properties.TestingRun(t)
}

func TestRegisterRejectsMismatchedWireProtocolBeforeSideEffects(t *testing.T) {
	t.Parallel()

	for _, version := range []int{0, toolregistry.WireProtocolVersion + 1} {
		streams := newMockStreamManagerForService()
		health := newMockHealthTracker()
		svc, err := newTestServiceForServiceTests(mockpulse.NewClient(t), streams, health)
		require.NoError(t, err)

		payload := validRegisterPayloadForSchemaAdmission("wire-version-mismatch")
		payload.WireProtocolVersion = version
		_, err = svc.Register(context.Background(), payload)
		require.Error(t, err)

		var serviceErr *goa.ServiceError
		require.ErrorAs(t, err, &serviceErr)
		assert.Equal(t, "validation_error", serviceErr.Name)
		assert.Empty(t, streams.createdToolsets)
		assert.Empty(t, health.startedToolsets)
	}
}

func TestRegisterRejectsSemanticallyInvalidSchemaWithoutSideEffects(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name   string
		mutate func(*genregistry.ToolSchema)
	}{
		{
			name: "payload schema",
			mutate: func(tool *genregistry.ToolSchema) {
				tool.PayloadSchema = []byte(`{"type":"definitely-not-a-json-schema-type"}`)
			},
		},
		{
			name: "result schema",
			mutate: func(tool *genregistry.ToolSchema) {
				tool.ResultSchema = []byte(`{"properties":{"value":{"type":"not-a-real-type"}}}`)
			},
		},
		{
			name: "sidecar schema",
			mutate: func(tool *genregistry.ToolSchema) {
				tool.SidecarSchema = []byte(`{"properties":{"meta":{"type":"not-a-real-type"}}}`)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pulseClient := mockpulse.NewClient(t)
			streams := newMockStreamManagerForService()
			health := newMockHealthTracker()

			svc, err := newTestServiceForServiceTests(pulseClient, streams, health)
			require.NoError(t, err)

			payload := validRegisterPayloadForSchemaAdmission("toolset-" + tc.name)
			tc.mutate(payload.Tools[0])

			_, err = svc.Register(ctx, payload)
			require.Error(t, err)

			var svcErr *goa.ServiceError
			require.ErrorAs(t, err, &svcErr)
			require.Equal(t, "validation_error", svcErr.Name)
			require.Empty(t, streams.createdToolsets)
			require.Empty(t, health.startedToolsets)

			list, err := svc.ListToolsets(ctx, &genregistry.ListToolsetsPayload{})
			require.NoError(t, err)
			require.Empty(t, list.Toolsets)
		})
	}
}

// invalidSchemaTestCase represents a test case for invalid schema rejection.
type invalidSchemaTestCase struct {
	payload *genregistry.RegisterPayload
}

func validRegisterPayloadForSchemaAdmission(name string) *genregistry.RegisterPayload {
	description := "schema admission test toolset"
	version := genregistry.SemVer("1.0.0")

	return &genregistry.RegisterPayload{
		Name:                  name,
		Description:           &description,
		Version:               &version,
		Tags:                  []string{"schema"},
		ProviderID:            name + "/provider-a",
		ProviderIncarnationID: testIncarnationA,
		AdmissionRevision:     testAdmissionRevisionA,
		WireProtocolVersion:   toolregistry.WireProtocolVersion,
		Tools: []*genregistry.ToolSchema{
			{
				Name:          "lookup",
				PayloadSchema: []byte(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
				ResultSchema:  []byte(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`),
			},
		},
	}
}

// genInvalidSchemaTestCase generates test cases with invalid tool schemas.
func genInvalidSchemaTestCase() gopter.Gen {
	return gopter.CombineGens(
		genToolsetNameForInvalidSchema(),
		genInvalidSchemaType(),
	).FlatMap(func(vals any) gopter.Gen {
		arr := vals.([]any)
		toolsetName := arr[0].(string)
		invalidType := arr[1].(string)

		return genInvalidToolSchema(invalidType).Map(func(tools []*genregistry.ToolSchema) invalidSchemaTestCase {
			desc := "A test toolset"
			rawVersion := "1.0.0"
			version := genregistry.SemVer(rawVersion)
			return invalidSchemaTestCase{
				payload: &genregistry.RegisterPayload{
					Name:                  toolsetName,
					Description:           &desc,
					Version:               &version,
					Tags:                  []string{"test"},
					Tools:                 tools,
					ProviderID:            toolsetName + "/provider-a",
					ProviderIncarnationID: testIncarnationA,
					AdmissionRevision:     testAdmissionRevisionA,
					WireProtocolVersion:   toolregistry.WireProtocolVersion,
				},
			}
		})
	}, reflect.TypeOf(invalidSchemaTestCase{}))
}

// genToolsetNameForInvalidSchema generates unique toolset names for invalid schema tests.
func genToolsetNameForInvalidSchema() gopter.Gen {
	return gen.Identifier().Map(func(s string) string {
		return "invalid-schema-test-" + s
	})
}

// genInvalidSchemaType generates types of invalid schemas to test.
func genInvalidSchemaType() gopter.Gen {
	return gen.OneConstOf(
		"empty-input-schema",
		"invalid-json-input",
		"invalid-json-output",
	)
}

// genInvalidToolSchema generates tool schemas with the specified type of invalidity.
func genInvalidToolSchema(invalidType string) gopter.Gen {
	return gen.Identifier().Map(func(toolName string) []*genregistry.ToolSchema {
		desc := "A test tool"
		switch invalidType {
		case "empty-input-schema":
			// Empty input schema (required but missing).
			return []*genregistry.ToolSchema{
				{
					Name:          toolName,
					Description:   &desc,
					PayloadSchema: []byte{}, // Empty
					ResultSchema:  []byte(`{"type":"object"}`),
				},
			}
		case "invalid-json-input":
			// Invalid JSON in input schema.
			return []*genregistry.ToolSchema{
				{
					Name:          toolName,
					Description:   &desc,
					PayloadSchema: []byte(`{not valid json`),
					ResultSchema:  []byte(`{"type":"object"}`),
				},
			}
		case "invalid-json-output":
			// Invalid JSON in output schema.
			return []*genregistry.ToolSchema{
				{
					Name:          toolName,
					Description:   &desc,
					PayloadSchema: []byte(`{"type":"object"}`),
					ResultSchema:  []byte(`{not valid json`),
				},
			}
		default:
			// Fallback to empty input schema.
			return []*genregistry.ToolSchema{
				{
					Name:          toolName,
					Description:   &desc,
					PayloadSchema: []byte{},
					ResultSchema:  nil,
				},
			}
		}
	})
}

// TestUnregisterAbsentGenerationIsIdempotent verifies that retrying cleanup
// after exact catalog deletion succeeds for the same expected token.
func TestUnregisterAbsentGenerationIsIdempotent(t *testing.T) {
	rdb := getRedis(t)
	pulseClient, err := clientspulse.New(clientspulse.Options{Redis: rdb})
	if err != nil {
		t.Fatalf("create pulse client: %v", err)
	}

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("unregistering an absent expected generation is idempotent", prop.ForAll(
		func(tc unregisterAbsentTestCase) bool {
			ctx := context.Background()

			// Create mock dependencies.
			mockSM := newMockStreamManagerForService()
			mockHT := newMockHealthTracker()

			// Create the service.
			svc, err := newTestServiceForServiceTests(pulseClient, mockSM, mockHT)
			if err != nil {
				return false
			}

			// Register any existing toolsets (to ensure the target is not among them).
			for _, payload := range tc.existingToolsets {
				_, err := svc.Register(ctx, payload)
				if err != nil {
					return false
				}
			}

			// Retry unregister for an expected generation whose catalog is absent.
			err = svc.Unregister(ctx, &genregistry.UnregisterPayload{
				Name:                      tc.nonExistentName,
				ExpectedRegistrationToken: "absent-registration-token",
			})
			return err == nil
		},
		genUnregisterAbsentTestCase(),
	))

	properties.TestingRun(t)
}

// unregisterAbsentTestCase describes an absent expected generation alongside
// unrelated catalog registrations.
type unregisterAbsentTestCase struct {
	existingToolsets []*genregistry.RegisterPayload
	nonExistentName  string
}

// genUnregisterAbsentTestCase generates absent expected-generation cases.
func genUnregisterAbsentTestCase() gopter.Gen {
	return gen.Identifier().Map(func(baseName string) unregisterAbsentTestCase {
		return unregisterAbsentTestCase{
			existingToolsets: nil,
			nonExistentName:  fmt.Sprintf("non-existent-%s", baseName),
		}
	})
}
