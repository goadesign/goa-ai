package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	goa "goa.design/goa/v3/pkg"

	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/registry/store/memory"
	"goa.design/goa-ai/runtime/toolregistry"
)

// TestRegistrationIdempotence verifies Property 2: Registration idempotence.
// **Feature: internal-tool-registry, Property 2: Registration idempotence**
// *For any* toolset, registering it twice with updated metadata should result in
// the second metadata being stored, and the stream ID should remain the same.
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

	properties.Property("registering twice updates metadata and preserves stream ID", prop.ForAll(
		func(tc registrationIdempotenceTestCase) bool {
			ctx := context.Background()

			// Create a memory store.
			store := memory.New()

			// Create mock dependencies.
			mockSM := newMockStreamManagerForService()
			mockHT := newMockHealthTracker()

			// Create the service.
			svc, err := NewService(ServiceOptions{
				Store:         store,
				StreamManager: mockSM,
				HealthTracker: mockHT,
				PulseClient:   pulseClient,
			})
			if err != nil {
				return false
			}

			// First registration.
			result1, err := svc.Register(ctx, tc.firstPayload)
			if err != nil {
				return false
			}
			firstStreamID := result1.StreamID

			// Second registration with updated metadata.
			result2, err := svc.Register(ctx, tc.secondPayload)
			if err != nil {
				return false
			}
			secondStreamID := result2.StreamID

			// Stream ID should remain the same.
			if firstStreamID != secondStreamID {
				return false
			}

			// Retrieve the toolset and verify it has the second registration's metadata.
			retrieved, err := svc.GetToolset(ctx, &genregistry.GetToolsetPayload{
				Name: tc.firstPayload.Name,
			})
			if err != nil {
				return false
			}

			// Verify the metadata matches the second registration.
			if !stringPtrEqualForService(retrieved.Description, tc.secondPayload.Description) {
				return false
			}
			if !stringPtrEqualForService(retrieved.Version, tc.secondPayload.Version) {
				return false
			}
			if !stringSliceEqualForService(retrieved.Tags, tc.secondPayload.Tags) {
				return false
			}
			if len(retrieved.Tools) != len(tc.secondPayload.Tools) {
				return false
			}

			return true
		},
		genRegistrationIdempotenceTestCase(),
	))

	properties.TestingRun(t)
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
		return gopter.CombineGens(
			genRegisterPayload(toolsetName),
			genRegisterPayload(toolsetName),
		).Map(func(vals []any) registrationIdempotenceTestCase {
			return registrationIdempotenceTestCase{
				firstPayload:  vals[0].(*genregistry.RegisterPayload),
				secondPayload: vals[1].(*genregistry.RegisterPayload),
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
			Name:        name,
			Description: desc,
			Version:     version,
			Tags:        vals[2].([]string),
			Tools:       vals[3].([]*genregistry.ToolSchema),
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

			// Create a memory store and save the toolset.
			store := memory.New()
			if err := store.SaveToolset(ctx, tc.toolset); err != nil {
				return false
			}

			// Create mock dependencies.
			mockSM := newMockStreamManagerForService()
			mockHT := newMockHealthTracker() // Always healthy

			// Create the service.
			svc, err := NewService(ServiceOptions{
				Store:         store,
				StreamManager: mockSM,
				HealthTracker: mockHT,
				PulseClient:   pulseClient,
			})
			if err != nil {
				return false
			}

			// Call the tool with an invalid payload.
			_, err = svc.CallTool(ctx, &genregistry.CallToolPayload{
				Toolset:     tc.toolset.Name,
				Tool:        tc.toolName,
				PayloadJSON: tc.invalidPayload,
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
				StreamID:     "toolset:" + toolsetName + ":requests",
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
	mu       sync.RWMutex
	messages map[string][]toolregistry.ToolCallMessage
}

func newMockStreamManagerForService() *mockStreamManagerForService {
	return &mockStreamManagerForService{
		messages: make(map[string][]toolregistry.ToolCallMessage),
	}
}

func (m *mockStreamManagerForService) GetOrCreateStream(ctx context.Context, toolset string) (clientspulse.Stream, string, error) {
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

var _ StreamManager = (*mockStreamManagerForService)(nil)

// mockHealthTracker is a mock HealthTracker for service tests.
type mockHealthTracker struct {
	healthy bool
}

func newMockHealthTracker() *mockHealthTracker {
	return &mockHealthTracker{healthy: true}
}

func (m *mockHealthTracker) RecordPong(ctx context.Context, toolset string) error {
	return nil
}

func (m *mockHealthTracker) IsHealthy(toolset string) bool {
	return m.healthy
}

func (m *mockHealthTracker) StartPingLoop(ctx context.Context, toolset string) error {
	return nil
}

func (m *mockHealthTracker) StopPingLoop(ctx context.Context, toolset string) {}

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

			// Create a memory store.
			store := memory.New()

			// Create mock dependencies.
			mockSM := newMockStreamManagerForService()
			mockHT := newMockHealthTracker()

			// Create the service.
			svc, err := NewService(ServiceOptions{
				Store:         store,
				StreamManager: mockSM,
				HealthTracker: mockHT,
				PulseClient:   pulseClient,
			})
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

			// Unregister the target toolset.
			err = svc.Unregister(ctx, &genregistry.UnregisterPayload{
				Name: tc.targetName,
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

			// Create a memory store.
			store := memory.New()

			// Create mock dependencies.
			mockSM := newMockStreamManagerForService()
			mockHT := newMockHealthTracker()

			// Create the service.
			svc, err := NewService(ServiceOptions{
				Store:         store,
				StreamManager: mockSM,
				HealthTracker: mockHT,
				PulseClient:   pulseClient,
			})
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

// invalidSchemaTestCase represents a test case for invalid schema rejection.
type invalidSchemaTestCase struct {
	payload *genregistry.RegisterPayload
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
					Name:        toolsetName,
					Description: &desc,
					Version:     &version,
					Tags:        []string{"test"},
					Tools:       tools,
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

// TestUnregisterNonExistentReturnsNotFound verifies Property 5: Unregister non-existent returns not-found.
// **Feature: internal-tool-registry, Property 5: Unregister non-existent returns not-found**
// *For any* toolset name that is not registered, unregistering should return a not-found error.
// **Validates: Requirements 5.2, 7.2**
func TestUnregisterNonExistentReturnsNotFound(t *testing.T) {
	rdb := getRedis(t)
	pulseClient, err := clientspulse.New(clientspulse.Options{Redis: rdb})
	if err != nil {
		t.Fatalf("create pulse client: %v", err)
	}

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("unregistering non-existent toolset returns not-found error", prop.ForAll(
		func(tc unregisterNonExistentTestCase) bool {
			ctx := context.Background()

			// Create a memory store.
			store := memory.New()

			// Create mock dependencies.
			mockSM := newMockStreamManagerForService()
			mockHT := newMockHealthTracker()

			// Create the service.
			svc, err := NewService(ServiceOptions{
				Store:         store,
				StreamManager: mockSM,
				HealthTracker: mockHT,
				PulseClient:   pulseClient,
			})
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

			// Attempt to unregister a non-existent toolset.
			err = svc.Unregister(ctx, &genregistry.UnregisterPayload{
				Name: tc.nonExistentName,
			})

			// Should return an error.
			if err == nil {
				return false
			}

			// Check that it's a not-found error.
			var svcErr *goa.ServiceError
			if !errors.As(err, &svcErr) {
				return false
			}
			return svcErr.Name == "not_found"
		},
		genUnregisterNonExistentTestCase(),
	))

	properties.TestingRun(t)
}

// unregisterNonExistentTestCase represents a test case for unregistering non-existent toolsets.
type unregisterNonExistentTestCase struct {
	existingToolsets []*genregistry.RegisterPayload
	nonExistentName  string
}

// genUnregisterNonExistentTestCase generates test cases for unregistering non-existent toolsets.
func genUnregisterNonExistentTestCase() gopter.Gen {
	// Generate a unique non-existent toolset name.
	return gen.Identifier().Map(func(baseName string) unregisterNonExistentTestCase {
		return unregisterNonExistentTestCase{
			existingToolsets: nil,
			nonExistentName:  fmt.Sprintf("non-existent-%s", baseName),
		}
	})
}
