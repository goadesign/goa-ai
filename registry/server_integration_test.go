//go:build integration

package registry

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	grpcclient "goa.design/goa-ai/registry/gen/grpc/registry/client"
	registrypb "goa.design/goa-ai/registry/gen/grpc/registry/pb"
	grpcserver "goa.design/goa-ai/registry/gen/grpc/registry/server"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	goa "goa.design/goa/v3/pkg"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

const (
	testToolsetName = "data-tools"
	errNotFound     = "not_found"
	errValidation   = "validation_error"
	errInvalidLen   = "invalid_length"
)

type callCountingService struct {
	genregistry.Service
	calls         atomic.Int64
	lastCall      atomic.Pointer[genregistry.CallToolPayload]
	callToolError error
}

// TestServerIntegration tests the full gRPC server stack using Goa's generated
// client and server code. It verifies the complete request/response cycle
// through the transport layer.
func TestServerIntegration(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	// Create the registry with test configuration.
	reg, err := New(ctx, Config{
		Redis:               rdb,
		Name:                "server-test-" + t.Name(),
		PingInterval:        50 * time.Millisecond,
		MissedPingThreshold: 2,
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	defer func() {
		if err := reg.Close(ctx); err != nil {
			t.Errorf("close registry: %v", err)
		}
	}()

	client, rawClient := startServiceAndClients(t, reg.Service())
	var testRegistrationToken string

	t.Run("empty discovery succeeds", func(t *testing.T) {
		listResult, err := client.ListToolsets(ctx, &genregistry.ListToolsetsPayload{})
		if err != nil {
			t.Fatalf("list empty registry: %v", err)
		}
		if len(listResult.Toolsets) != 0 {
			t.Errorf("expected 0 toolsets, got %d", len(listResult.Toolsets))
		}

		searchResult, err := client.Search(ctx, &genregistry.SearchPayload{
			Query: "missing",
		})
		if err != nil {
			t.Fatalf("search empty registry: %v", err)
		}
		if len(searchResult.Toolsets) != 0 {
			t.Errorf("expected 0 search results, got %d", len(searchResult.Toolsets))
		}
	})

	t.Run("register and list", func(t *testing.T) {
		desc := "Data processing tools"
		rawVersion := "1.0.0"
		version := genregistry.SemVer(rawVersion)

		// Register a toolset.
		regResult, err := client.Register(ctx, registerPayloadWithSchemaFingerprint(&genregistry.RegisterPayload{
			Name:                  testToolsetName,
			Description:           &desc,
			Version:               &version,
			Tags:                  []string{"data", "etl"},
			ProviderID:            "data-tools/provider-a",
			ProviderIncarnationID: testIncarnationA,
			AdmissionRevision:     testAdmissionRevisionA,
			WireProtocolVersion:   toolregistry.WireProtocolVersion,
			Tools: []*genregistry.ToolSchema{
				{
					Name:          "transform",
					Description:   strPtr("Transform data"),
					PayloadSchema: []byte(`{"type":"object","properties":{"input":{"type":"string"}}}`),
					ResultSchema:  []byte(`{"type":"object"}`),
				},
			},
		}))
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		if regResult.RegisteredAt == "" {
			t.Error("expected non-empty registration timestamp")
		}
		testRegistrationToken = regResult.RegistrationToken

		// List toolsets.
		listResult, err := client.ListToolsets(ctx, &genregistry.ListToolsetsPayload{})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(listResult.Toolsets) != 1 {
			t.Errorf("expected 1 toolset, got %d", len(listResult.Toolsets))
		}
		if listResult.Toolsets[0].Name != testToolsetName {
			t.Errorf("expected name %q, got %q", testToolsetName, listResult.Toolsets[0].Name)
		}
	})

	t.Run("check exact admission through gRPC", func(t *testing.T) {
		pending, err := client.CheckAdmission(ctx, &genregistry.CheckAdmissionPayload{
			Name:                      testToolsetName,
			ExpectedRegistrationToken: testRegistrationToken,
		})
		require.NoError(t, err)
		assert.False(t, pending.Ready)

		entry, err := reg.service.catalog.ActiveRegistration(ctx, testToolsetName)
		require.NoError(t, err)
		err = reg.service.catalog.RecordPong(
			ctx,
			testToolsetName,
			"data-tools/provider-a",
			testIncarnationA,
			entry.RegistrationToken,
			entry.HealthEpoch,
		)
		require.NoError(t, err)

		ready, err := client.CheckAdmission(ctx, &genregistry.CheckAdmissionPayload{
			Name:                      testToolsetName,
			ExpectedRegistrationToken: testRegistrationToken,
		})
		require.NoError(t, err)
		assert.True(t, ready.Ready)

		different, err := client.CheckAdmission(ctx, &genregistry.CheckAdmissionPayload{
			Name:                      testToolsetName,
			ExpectedRegistrationToken: testStaleToken,
		})
		require.NoError(t, err)
		assert.False(t, different.Ready)

		_, err = rawClient.CheckAdmission(ctx, &registrypb.CheckAdmissionRequest{
			Name:                      strPtr(testToolsetName),
			ExpectedRegistrationToken: strPtr("invalid"),
		})
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("get toolset", func(t *testing.T) {
		toolset, err := client.GetToolset(ctx, &genregistry.GetToolsetPayload{
			Name: testToolsetName,
		})
		if err != nil {
			t.Fatalf("get toolset: %v", err)
		}
		if toolset.Name != testToolsetName {
			t.Errorf("expected name %q, got %q", testToolsetName, toolset.Name)
		}
		if len(toolset.Tools) != 1 {
			t.Errorf("expected 1 tool, got %d", len(toolset.Tools))
		}
		if toolset.Tools[0].Name != "transform" {
			t.Errorf("expected tool 'transform', got %q", toolset.Tools[0].Name)
		}
	})

	t.Run("get toolset not found", func(t *testing.T) {
		_, err := client.GetToolset(ctx, &genregistry.GetToolsetPayload{
			Name: "nonexistent",
		})
		if err == nil {
			t.Fatal("expected error for nonexistent toolset")
		}
		var svcErr *goa.ServiceError
		if !errors.As(err, &svcErr) {
			t.Fatalf("expected ServiceError, got %T", err)
		}
		if svcErr.Name != errNotFound {
			t.Errorf("expected %q error, got %q", errNotFound, svcErr.Name)
		}
	})

	t.Run("search", func(t *testing.T) {
		searchResult, err := client.Search(ctx, &genregistry.SearchPayload{
			Query: "data",
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(searchResult.Toolsets) != 1 {
			t.Errorf("expected 1 result, got %d", len(searchResult.Toolsets))
		}
	})

	t.Run("filter by tags", func(t *testing.T) {
		// Register another toolset with different tags.
		desc := "Analytics tools"
		_, err := client.Register(ctx, registerPayloadWithSchemaFingerprint(&genregistry.RegisterPayload{
			Name:                  "analytics-tools",
			Description:           &desc,
			Tags:                  []string{"analytics", "reporting"},
			ProviderID:            "analytics-tools/provider-a",
			ProviderIncarnationID: testIncarnationA,
			AdmissionRevision:     testAdmissionRevisionA,
			WireProtocolVersion:   toolregistry.WireProtocolVersion,
			Tools: []*genregistry.ToolSchema{
				{
					Name:          "report",
					PayloadSchema: []byte(`{"type":"object"}`),
					ResultSchema:  []byte(`{"type":"object"}`),
				},
			},
		}))
		if err != nil {
			t.Fatalf("register analytics: %v", err)
		}

		// Filter by 'etl' tag should only return data-tools.
		listResult, err := client.ListToolsets(ctx, &genregistry.ListToolsetsPayload{
			Tags: []string{"etl"},
		})
		if err != nil {
			t.Fatalf("list with tags: %v", err)
		}
		if len(listResult.Toolsets) != 1 {
			t.Errorf("expected 1 toolset with 'etl' tag, got %d", len(listResult.Toolsets))
		}
		if listResult.Toolsets[0].Name != testToolsetName {
			t.Errorf("expected %q, got %q", testToolsetName, listResult.Toolsets[0].Name)
		}
	})

	t.Run("release provider is exact and idempotent", func(t *testing.T) {
		err := client.ReleaseProvider(ctx, &genregistry.ReleaseProviderPayload{
			Name:                      testToolsetName,
			ProviderID:                "data-tools/provider-a",
			ProviderIncarnationID:     testIncarnationA,
			ExpectedRegistrationToken: testStaleToken,
		})
		require.NoError(t, err)
		err = client.ReleaseProvider(ctx, &genregistry.ReleaseProviderPayload{
			Name:                      testToolsetName,
			ProviderID:                "data-tools/provider-a",
			ProviderIncarnationID:     testIncarnationA,
			ExpectedRegistrationToken: testRegistrationToken,
		})
		require.NoError(t, err)
		err = client.ReleaseProvider(ctx, &genregistry.ReleaseProviderPayload{
			Name:                      testToolsetName,
			ProviderID:                "data-tools/provider-a",
			ProviderIncarnationID:     testIncarnationA,
			ExpectedRegistrationToken: testRegistrationToken,
		})
		require.NoError(t, err)
	})

	t.Run("unregister", func(t *testing.T) {
		err := client.Unregister(ctx, &genregistry.UnregisterPayload{
			Name:                      testToolsetName,
			ExpectedRegistrationToken: testRegistrationToken,
		})
		if err != nil {
			t.Fatalf("unregister: %v", err)
		}

		// Verify it's gone.
		_, err = client.GetToolset(ctx, &genregistry.GetToolsetPayload{
			Name: testToolsetName,
		})
		if err == nil {
			t.Error("expected error after unregister")
		}
	})

	t.Run("unregister absent generation is idempotent", func(t *testing.T) {
		err := client.Unregister(ctx, &genregistry.UnregisterPayload{
			Name:                      "nonexistent",
			ExpectedRegistrationToken: testStaleToken,
		})
		if err != nil {
			t.Fatalf("idempotent unregister: %v", err)
		}
	})
}

// TestServerMultiNodeSync tests that two registry nodes sharing the same Redis
// and store see consistent state through the gRPC interface.
func TestServerMultiNodeSync(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	// Create two registry nodes with the same name (cluster) and shared Redis-backed catalog.
	clusterName := "cluster-test-" + t.Name()

	reg1, err := New(ctx, Config{
		Redis:               rdb,
		Name:                clusterName,
		PingInterval:        50 * time.Millisecond,
		MissedPingThreshold: 2,
	})
	if err != nil {
		t.Fatalf("create registry 1: %v", err)
	}
	defer func() { _ = reg1.Close(ctx) }()

	reg2, err := New(ctx, Config{
		Redis:               rdb,
		Name:                clusterName,
		PingInterval:        50 * time.Millisecond,
		MissedPingThreshold: 2,
	})
	if err != nil {
		t.Fatalf("create registry 2: %v", err)
	}
	defer func() { _ = reg2.Close(ctx) }()

	// Start gRPC servers for both nodes.
	client1 := startServerAndClient(t, reg1)
	client2 := startServerAndClient(t, reg2)

	// Register on node 1.
	desc := "Shared toolset"
	registration, err := client1.Register(ctx, registerPayloadWithSchemaFingerprint(&genregistry.RegisterPayload{
		Name:                  "shared-tools",
		Description:           &desc,
		Tags:                  []string{"shared"},
		ProviderID:            "shared-tools/provider-a",
		ProviderIncarnationID: testIncarnationA,
		AdmissionRevision:     testAdmissionRevisionA,
		WireProtocolVersion:   toolregistry.WireProtocolVersion,
		Tools: []*genregistry.ToolSchema{
			{
				Name:          "shared-tool",
				PayloadSchema: []byte(`{"type":"object"}`),
				ResultSchema:  []byte(`{"type":"object"}`),
			},
		},
	}))
	if err != nil {
		t.Fatalf("register on node 1: %v", err)
	}

	// Query from node 2 - should see the toolset (shared catalog).
	listResult, err := client2.ListToolsets(ctx, &genregistry.ListToolsetsPayload{})
	if err != nil {
		t.Fatalf("list from node 2: %v", err)
	}
	if len(listResult.Toolsets) != 1 {
		t.Errorf("expected 1 toolset on node 2, got %d", len(listResult.Toolsets))
	}

	// Unregister from node 2.
	err = client2.Unregister(ctx, &genregistry.UnregisterPayload{
		Name:                      "shared-tools",
		ExpectedRegistrationToken: registration.RegistrationToken,
	})
	if err != nil {
		t.Fatalf("unregister from node 2: %v", err)
	}

	// Query from node 1 - should be gone (shared catalog).
	listResult, err = client1.ListToolsets(ctx, &genregistry.ListToolsetsPayload{})
	if err != nil {
		t.Fatalf("list from node 1: %v", err)
	}
	if len(listResult.Toolsets) != 0 {
		t.Errorf("expected 0 toolsets on node 1 after unregister, got %d", len(listResult.Toolsets))
	}
}

// TestServerValidationErrors tests that the server properly returns validation
// errors through the gRPC transport.
func TestServerValidationErrors(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()

	reg, err := New(ctx, Config{
		Redis:               rdb,
		Name:                "validation-test-" + t.Name(),
		PingInterval:        50 * time.Millisecond,
		MissedPingThreshold: 2,
	})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	defer func() { _ = reg.Close(ctx) }()

	client := startServerAndClient(t, reg)

	t.Run("invalid schema rejected", func(t *testing.T) {
		_, err := client.Register(ctx, &genregistry.RegisterPayload{
			Name:                  "bad-schema-tools",
			ProviderID:            "bad-schema-tools/provider-a",
			ProviderIncarnationID: testIncarnationA,
			AdmissionRevision:     testAdmissionRevisionA,
			WireProtocolVersion:   toolregistry.WireProtocolVersion,
			SchemaFingerprint:     testActiveRegistrationToken,
			Tools: []*genregistry.ToolSchema{
				{
					Name:          "bad-tool",
					PayloadSchema: []byte(`{not valid json`),
					ResultSchema:  []byte(`{"type":"object"}`),
				},
			},
		})
		if err == nil {
			t.Fatal("expected error for invalid schema")
		}
		var svcErr *goa.ServiceError
		if !errors.As(err, &svcErr) {
			t.Fatalf("expected ServiceError, got %T: %v", err, err)
		}
		if svcErr.Name != errValidation {
			t.Errorf("expected %q, got %q", errValidation, svcErr.Name)
		}
	})

	t.Run("empty input schema rejected", func(t *testing.T) {
		_, err := client.Register(ctx, &genregistry.RegisterPayload{
			Name:                  "empty-schema-tools",
			ProviderID:            "empty-schema-tools/provider-a",
			ProviderIncarnationID: testIncarnationA,
			AdmissionRevision:     testAdmissionRevisionA,
			WireProtocolVersion:   toolregistry.WireProtocolVersion,
			SchemaFingerprint:     testActiveRegistrationToken,
			Tools: []*genregistry.ToolSchema{
				{
					Name:          "empty-tool",
					PayloadSchema: []byte{},
					ResultSchema:  []byte(`{"type":"object"}`),
				},
			},
		})
		if err == nil {
			t.Fatal("expected error for empty schema")
		}
		var svcErr *goa.ServiceError
		if !errors.As(err, &svcErr) {
			t.Fatalf("expected ServiceError, got %T: %v", err, err)
		}
		if svcErr.Name != errInvalidLen {
			t.Errorf("expected %q, got %q", errInvalidLen, svcErr.Name)
		}
	})

	t.Run("missing admission revision rejected", func(t *testing.T) {
		_, err := client.Register(ctx, &genregistry.RegisterPayload{
			Name:                  "missing-admission-revision",
			ProviderID:            "missing-admission-revision/provider-a",
			ProviderIncarnationID: testIncarnationA,
			WireProtocolVersion:   toolregistry.WireProtocolVersion,
			SchemaFingerprint:     testActiveRegistrationToken,
			Tools: []*genregistry.ToolSchema{{
				Name:          "lookup",
				PayloadSchema: []byte(`{"type":"object"}`),
				ResultSchema:  []byte(`{"type":"object"}`),
			}},
		})
		require.ErrorContains(t, err, "admission_revision")
	})

	t.Run("malformed admission revision rejected", func(t *testing.T) {
		_, err := client.Register(ctx, &genregistry.RegisterPayload{
			Name:                  "malformed-admission-revision",
			ProviderID:            "malformed-admission-revision/provider-a",
			ProviderIncarnationID: testIncarnationA,
			AdmissionRevision:     "contains whitespace",
			WireProtocolVersion:   toolregistry.WireProtocolVersion,
			SchemaFingerprint:     testActiveRegistrationToken,
			Tools: []*genregistry.ToolSchema{{
				Name:          "lookup",
				PayloadSchema: []byte(`{"type":"object"}`),
				ResultSchema:  []byte(`{"type":"object"}`),
			}},
		})
		require.ErrorContains(t, err, "admission_revision")
	})

	t.Run("missing wire protocol version rejected", func(t *testing.T) {
		_, err := client.Register(ctx, &genregistry.RegisterPayload{
			Name:                  "missing-wire-protocol-version",
			ProviderID:            "missing-wire-protocol-version/provider-a",
			ProviderIncarnationID: testIncarnationA,
			AdmissionRevision:     testAdmissionRevisionA,
			SchemaFingerprint:     testActiveRegistrationToken,
			Tools: []*genregistry.ToolSchema{{
				Name:          "lookup",
				PayloadSchema: []byte(`{"type":"object"}`),
				ResultSchema:  []byte(`{"type":"object"}`),
			}},
		})
		require.ErrorContains(t, err, "wire_protocol_version")
	})

	t.Run("noncanonical registration token rejected", func(t *testing.T) {
		err := client.Unregister(ctx, &genregistry.UnregisterPayload{
			Name:                      "malformed-registration-token",
			ExpectedRegistrationToken: "ABC123",
		})
		require.ErrorContains(t, err, "expected_registration_token")
	})
}

func TestServerGRPCStatusMappingsAndToolCallIDBoundary(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()
	reg, err := New(ctx, Config{
		Redis:               rdb,
		Name:                "grpc-status-test-" + t.Name(),
		PingInterval:        time.Hour,
		MissedPingThreshold: 2,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(ctx)) })

	counting := &callCountingService{Service: reg.Service()}
	generatedClient, rawClient := startServiceAndClients(t, counting)
	first, err := rawClient.Register(ctx, grpcRegisterRequest(
		"status-tools",
		"admission-a",
		testAdmissionRevisionA,
		"provider-a",
	))
	require.NoError(t, err)
	wireProtocolVersion := int32(toolregistry.WireProtocolVersion)

	_, err = rawClient.Register(ctx, grpcRegisterRequest(
		"status-tools",
		"admission-b",
		testAdmissionRevisionB,
		"provider-b",
	))
	assert.Equal(t, codes.Unavailable, status.Code(err))

	_, err = rawClient.Unregister(ctx, &registrypb.UnregisterRequest{
		Name:                      strPtr("status-tools"),
		ExpectedRegistrationToken: strPtr(testStaleToken),
	})
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))

	_, err = rawClient.Unregister(ctx, &registrypb.UnregisterRequest{
		Name:                      strPtr("status-tools"),
		ExpectedRegistrationToken: strPtr(first.GetRegistrationToken()),
	})
	require.NoError(t, err)
	_, err = rawClient.Register(ctx, grpcRegisterRequest(
		"status-tools",
		"admission-a",
		testAdmissionRevisionA,
		"provider-a",
	))
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))

	_, err = rawClient.CallTool(ctx, &registrypb.CallToolRequest{
		Toolset:     strPtr("status-tools"),
		Tool:        strPtr("status.lookup"),
		PayloadJson: []byte(`{}`),
		Meta: &registrypb.ToolCallMeta{
			RunId:      strPtr("run-1"),
			SessionId:  strPtr("session-1"),
			ToolCallId: strPtr("old-consumer-call"),
		},
	})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Zero(t, counting.calls.Load(), "old consumer must be rejected before service invocation")

	overlongID := strings.Repeat("x", 257)
	_, err = rawClient.CallTool(ctx, &registrypb.CallToolRequest{
		Toolset:             strPtr("status-tools"),
		Tool:                strPtr("status.lookup"),
		PayloadJson:         []byte(`{}`),
		WireProtocolVersion: &wireProtocolVersion,
		Meta: &registrypb.ToolCallMeta{
			RunId:      strPtr("run-1"),
			SessionId:  strPtr("session-1"),
			ToolCallId: strPtr(overlongID),
		},
	})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Zero(t, counting.calls.Load(), "invalid tool_call_id must be rejected before publication")

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = rawClient.CheckAdmission(canceledCtx, &registrypb.CheckAdmissionRequest{
		Name:                      strPtr("status-tools"),
		ExpectedRegistrationToken: strPtr(first.GetRegistrationToken()),
	})
	assert.Equal(t, codes.Canceled, status.Code(err))
	_, err = generatedClient.CheckAdmission(canceledCtx, &genregistry.CheckAdmissionPayload{
		Name:                      "status-tools",
		ExpectedRegistrationToken: first.GetRegistrationToken(),
	})
	require.ErrorIs(t, err, context.Canceled)

	rejected := &callCountingService{
		Service:       reg.Service(),
		callToolError: genregistry.MakeCallNotAdmitted(errors.New("no healthy providers")),
	}
	generatedClient, rejectedRawClient := startServiceAndClients(t, rejected)
	_, err = generatedClient.CallTool(ctx, &genregistry.CallToolPayload{
		Toolset:             "status-tools",
		Tool:                "status.lookup",
		PayloadJSON:         []byte(`{}`),
		WireProtocolVersion: toolregistry.WireProtocolVersion,
		Meta: &genregistry.ToolCallMeta{
			RunID:      "run-1",
			SessionID:  "session-1",
			ToolCallID: "rejected-call",
			Labels:     map[string]string{"facility": "allentown"},
		},
	})
	var serviceErr *goa.ServiceError
	require.ErrorAs(t, err, &serviceErr)
	assert.Equal(t, "call_not_admitted", serviceErr.Name)
	require.Equal(t, map[string]string{"facility": "allentown"}, rejected.lastCall.Load().Meta.Labels)
	_, err = rejectedRawClient.CallTool(ctx, &registrypb.CallToolRequest{
		Toolset:             strPtr("status-tools"),
		Tool:                strPtr("status.lookup"),
		PayloadJson:         []byte(`{}`),
		WireProtocolVersion: &wireProtocolVersion,
		Meta: &registrypb.ToolCallMeta{
			RunId:      strPtr("run-1"),
			SessionId:  strPtr("session-1"),
			ToolCallId: strPtr("rejected-call"),
		},
	})
	assert.Equal(t, codes.Unavailable, status.Code(err))
}

func (s *callCountingService) CallTool(
	ctx context.Context,
	payload *genregistry.CallToolPayload,
) (*genregistry.CallToolResult, error) {
	s.calls.Add(1)
	s.lastCall.Store(payload)
	if s.callToolError != nil {
		return nil, s.callToolError
	}
	return s.Service.CallTool(ctx, payload)
}

// startServerAndClient starts a gRPC server for the registry and returns a
// connected Goa client. The server is stopped when the test completes.
func startServerAndClient(t *testing.T, reg *Registry) *genregistry.Client {
	t.Helper()
	client, _ := startServiceAndClients(t, reg.Service())
	return client
}

func startServiceAndClients(
	t *testing.T,
	service genregistry.Service,
) (*genregistry.Client, registrypb.RegistryClient) {
	t.Helper()

	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	endpoints := genregistry.NewEndpoints(service)
	registrypb.RegisterRegistryServer(grpcServer, grpcserver.New(endpoints, nil))

	go func() {
		_ = grpcServer.Serve(lis)
	}()
	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	grpcCli := grpcclient.NewClient(conn)
	client := genregistry.NewClient(
		grpcCli.Register(),
		grpcCli.ReleaseProvider(),
		grpcCli.DrainProvider(),
		grpcCli.Unregister(),
		grpcCli.Pong(),
		grpcCli.ListToolsets(),
		grpcCli.GetToolset(),
		grpcCli.CheckAdmission(),
		grpcCli.Search(),
		grpcCli.CallTool(),
		grpcCli.RetryTool(),
		grpcCli.CompleteToolCall(),
		grpcCli.PublishToolOutputDelta(),
		grpcCli.ReportToolCallOverload(),
		grpcCli.ClaimToolCall(),
	)
	return client, registrypb.NewRegistryClient(conn)
}

func grpcRegisterRequest(
	name, description, revision, providerID string,
) *registrypb.RegisterRequest {
	wireProtocolVersion := int32(toolregistry.WireProtocolVersion)
	request := &registrypb.RegisterRequest{
		Name:                  strPtr(name),
		Description:           &description,
		ProviderId:            strPtr(providerID),
		ProviderIncarnationId: strPtr(testIncarnationA),
		AdmissionRevision:     strPtr(revision),
		WireProtocolVersion:   &wireProtocolVersion,
		Tools: []*registrypb.ToolSchema{{
			Name:          strPtr("status.lookup"),
			PayloadSchema: []byte(`{"type":"object"}`),
			ResultSchema:  []byte(`{"type":"object"}`),
		}},
	}
	request.SchemaFingerprint = strPtr(toolsetSchemaFingerprint(&genregistry.Toolset{
		Name:        name,
		Description: &description,
		Tools: []*genregistry.ToolSchema{{
			Name:          "status.lookup",
			PayloadSchema: []byte(`{"type":"object"}`),
			ResultSchema:  []byte(`{"type":"object"}`),
		}},
	}))
	return request
}

func strPtr(s string) *string {
	return &s
}
