package registry

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockRegistrationClient implements RegistrationClient for testing.
type mockRegistrationClient struct {
	mu              sync.Mutex
	registerCalls   int
	deregisterCalls int
	heartbeatCalls  int
	registerErr     error
	deregisterErr   error
	heartbeatErr    error
	registeredCards []*AgentCard
	deregisteredIDs []string
	heartbeatIDs    []string
}

func (m *mockRegistrationClient) Register(_ context.Context, card *AgentCard) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registerCalls++
	m.registeredCards = append(m.registeredCards, card)
	return m.registerErr
}

func (m *mockRegistrationClient) Deregister(_ context.Context, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deregisterCalls++
	m.deregisteredIDs = append(m.deregisteredIDs, agentID)
	return m.deregisterErr
}

func (m *mockRegistrationClient) Heartbeat(_ context.Context, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.heartbeatCalls++
	m.heartbeatIDs = append(m.heartbeatIDs, agentID)
	return m.heartbeatErr
}

func (m *mockRegistrationClient) getRegisterCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.registerCalls
}

func (m *mockRegistrationClient) getDeregisterCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deregisterCalls
}

func (m *mockRegistrationClient) getHeartbeatCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.heartbeatCalls
}

// TestNewRegistrationManager tests registration manager creation.
// **Validates: Requirements 3.1**
func TestNewRegistrationManager(t *testing.T) {
	t.Run("creates manager with defaults", func(t *testing.T) {
		m := NewRegistrationManager()
		if m == nil {
			t.Fatal("NewRegistrationManager returned nil")
		}
		if m.registrations == nil {
			t.Error("registrations map is nil")
		}
		if m.logger == nil {
			t.Error("logger is nil")
		}
		if m.obs == nil {
			t.Error("observability is nil")
		}
	})

	t.Run("creates manager with custom logger", func(t *testing.T) {
		logger := &testLogger{}
		m := NewRegistrationManager(WithRegistrationLogger(logger))
		if m.logger != logger {
			t.Error("custom logger not set")
		}
	})

	t.Run("creates manager with custom observability", func(t *testing.T) {
		obs := NewObservability(nil, nil, nil)
		m := NewRegistrationManager(WithRegistrationObservability(obs))
		if m.obs != obs {
			t.Error("custom observability not set")
		}
	})
}

// TestRegisterAgent tests the agent registration flow.
// **Validates: Requirements 3.1**
func TestRegisterAgent(t *testing.T) {
	ctx := context.Background()

	t.Run("registers agent successfully", func(t *testing.T) {
		m := NewRegistrationManager()
		client := &mockRegistrationClient{}
		card := &AgentCard{
			Name:            "test-agent",
			Description:     "A test agent",
			URL:             "https://example.com/agents/test",
			ProtocolVersion: "1.0",
		}

		err := m.RegisterAgent(ctx, "test-registry", client, card, RegistrationConfig{
			HeartbeatInterval: time.Hour, // Long interval to avoid heartbeat during test
		})
		if err != nil {
			t.Fatalf("RegisterAgent failed: %v", err)
		}

		// Verify client was called
		if client.getRegisterCalls() != 1 {
			t.Errorf("expected 1 register call, got %d", client.getRegisterCalls())
		}

		// Verify registration is tracked
		if !m.IsRegistered("test-registry", "test-agent") {
			t.Error("agent should be registered")
		}

		// Clean up
		_ = m.DeregisterAgent(ctx, "test-registry", "test-agent")
	})

	t.Run("returns error on duplicate registration", func(t *testing.T) {
		m := NewRegistrationManager()
		client := &mockRegistrationClient{}
		card := &AgentCard{Name: "dup-agent"}

		err := m.RegisterAgent(ctx, "test-registry", client, card, RegistrationConfig{
			HeartbeatInterval: time.Hour,
		})
		if err != nil {
			t.Fatalf("first RegisterAgent failed: %v", err)
		}

		// Try to register again
		err = m.RegisterAgent(ctx, "test-registry", client, card, RegistrationConfig{})
		if err == nil {
			t.Fatal("expected error on duplicate registration")
		}

		// Clean up
		_ = m.DeregisterAgent(ctx, "test-registry", "dup-agent")
	})

	t.Run("returns error when client fails", func(t *testing.T) {
		m := NewRegistrationManager()
		client := &mockRegistrationClient{
			registerErr: errors.New("registration failed"),
		}
		card := &AgentCard{Name: "fail-agent"}

		err := m.RegisterAgent(ctx, "test-registry", client, card, RegistrationConfig{})
		if err == nil {
			t.Fatal("expected error when client fails")
		}

		// Verify agent is not tracked
		if m.IsRegistered("test-registry", "fail-agent") {
			t.Error("failed registration should not be tracked")
		}
	})

	t.Run("uses default heartbeat interval", func(t *testing.T) {
		m := NewRegistrationManager()
		client := &mockRegistrationClient{}
		card := &AgentCard{Name: "default-interval-agent"}

		err := m.RegisterAgent(ctx, "test-registry", client, card, RegistrationConfig{
			HeartbeatInterval: 0, // Should default to 30s
		})
		if err != nil {
			t.Fatalf("RegisterAgent failed: %v", err)
		}

		// Verify registration exists
		if !m.IsRegistered("test-registry", "default-interval-agent") {
			t.Error("agent should be registered")
		}

		// Clean up
		_ = m.DeregisterAgent(ctx, "test-registry", "default-interval-agent")
	})
}

// TestDeregisterAgent tests the agent deregistration flow.
// **Validates: Requirements 3.4**
func TestDeregisterAgent(t *testing.T) {
	ctx := context.Background()

	t.Run("deregisters agent successfully", func(t *testing.T) {
		m := NewRegistrationManager()
		client := &mockRegistrationClient{}
		card := &AgentCard{Name: "dereg-agent"}

		// Register first
		err := m.RegisterAgent(ctx, "test-registry", client, card, RegistrationConfig{
			HeartbeatInterval: time.Hour,
		})
		if err != nil {
			t.Fatalf("RegisterAgent failed: %v", err)
		}

		// Deregister
		err = m.DeregisterAgent(ctx, "test-registry", "dereg-agent")
		if err != nil {
			t.Fatalf("DeregisterAgent failed: %v", err)
		}

		// Verify client was called
		if client.getDeregisterCalls() != 1 {
			t.Errorf("expected 1 deregister call, got %d", client.getDeregisterCalls())
		}

		// Verify registration is removed
		if m.IsRegistered("test-registry", "dereg-agent") {
			t.Error("agent should not be registered after deregistration")
		}
	})

	t.Run("returns error for unknown agent", func(t *testing.T) {
		m := NewRegistrationManager()

		err := m.DeregisterAgent(ctx, "test-registry", "unknown-agent")
		if err == nil {
			t.Fatal("expected error for unknown agent")
		}
	})

	t.Run("returns error when client fails", func(t *testing.T) {
		m := NewRegistrationManager()
		client := &mockRegistrationClient{
			deregisterErr: errors.New("deregistration failed"),
		}
		card := &AgentCard{Name: "fail-dereg-agent"}

		// Register first
		err := m.RegisterAgent(ctx, "test-registry", client, card, RegistrationConfig{
			HeartbeatInterval: time.Hour,
		})
		if err != nil {
			t.Fatalf("RegisterAgent failed: %v", err)
		}

		// Deregister should fail
		err = m.DeregisterAgent(ctx, "test-registry", "fail-dereg-agent")
		if err == nil {
			t.Fatal("expected error when client fails")
		}

		// Registration should be removed even on client error
		if m.IsRegistered("test-registry", "fail-dereg-agent") {
			t.Error("registration should be removed even on client error")
		}
	})

	t.Run("stops heartbeat loop on deregistration", func(t *testing.T) {
		m := NewRegistrationManager()
		client := &mockRegistrationClient{}
		card := &AgentCard{Name: "heartbeat-stop-agent"}

		// Register with short heartbeat interval
		err := m.RegisterAgent(ctx, "test-registry", client, card, RegistrationConfig{
			HeartbeatInterval: 50 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("RegisterAgent failed: %v", err)
		}

		// Wait for at least one heartbeat
		time.Sleep(80 * time.Millisecond)

		// Deregister - this should stop the heartbeat loop
		err = m.DeregisterAgent(ctx, "test-registry", "heartbeat-stop-agent")
		if err != nil {
			t.Fatalf("DeregisterAgent failed: %v", err)
		}

		// Record heartbeats immediately after deregistration completes
		heartbeatsBefore := client.getHeartbeatCalls()

		// Wait for what would be multiple heartbeat intervals
		time.Sleep(150 * time.Millisecond)
		heartbeatsAfter := client.getHeartbeatCalls()

		if heartbeatsAfter != heartbeatsBefore {
			t.Errorf("heartbeats should stop after deregistration: before=%d, after=%d",
				heartbeatsBefore, heartbeatsAfter)
		}
	})
}

// TestHeartbeat tests the heartbeat functionality.
// **Validates: Requirements 3.3**
func TestHeartbeat(t *testing.T) {
	ctx := context.Background()

	t.Run("sends heartbeats at configured interval", func(t *testing.T) {
		m := NewRegistrationManager()
		client := &mockRegistrationClient{}
		card := &AgentCard{Name: "heartbeat-agent"}

		err := m.RegisterAgent(ctx, "test-registry", client, card, RegistrationConfig{
			HeartbeatInterval: 50 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("RegisterAgent failed: %v", err)
		}

		// Wait for multiple heartbeats
		time.Sleep(180 * time.Millisecond)

		heartbeats := client.getHeartbeatCalls()
		// Should have at least 2-3 heartbeats in 180ms with 50ms interval
		if heartbeats < 2 {
			t.Errorf("expected at least 2 heartbeats, got %d", heartbeats)
		}

		// Clean up
		_ = m.DeregisterAgent(ctx, "test-registry", "heartbeat-agent")
	})

	t.Run("continues heartbeats on transient errors", func(t *testing.T) {
		m := NewRegistrationManager()
		var callCount atomic.Int32
		client := &mockRegistrationClientWithHeartbeatFunc{
			heartbeatFunc: func(_ context.Context, _ string) error {
				count := callCount.Add(1)
				if count == 1 {
					return errors.New("transient error")
				}
				return nil
			},
		}
		card := &AgentCard{Name: "resilient-agent"}

		err := m.RegisterAgent(ctx, "test-registry", client, card, RegistrationConfig{
			HeartbeatInterval: 30 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("RegisterAgent failed: %v", err)
		}

		// Wait for multiple heartbeats
		time.Sleep(120 * time.Millisecond)

		// Should have continued despite first error
		count := int(callCount.Load())
		if count < 2 {
			t.Errorf("expected at least 2 heartbeat attempts, got %d", count)
		}

		// Clean up
		_ = m.DeregisterAgent(ctx, "test-registry", "resilient-agent")
	})
}

// TestDeregisterAll tests deregistering all agents.
// **Validates: Requirements 3.4**
func TestDeregisterAll(t *testing.T) {
	ctx := context.Background()

	t.Run("deregisters all agents", func(t *testing.T) {
		m := NewRegistrationManager()
		client1 := &mockRegistrationClient{}
		client2 := &mockRegistrationClient{}

		// Register multiple agents
		err := m.RegisterAgent(ctx, "registry-1", client1, &AgentCard{Name: "agent-1"}, RegistrationConfig{
			HeartbeatInterval: time.Hour,
		})
		if err != nil {
			t.Fatalf("RegisterAgent 1 failed: %v", err)
		}

		err = m.RegisterAgent(ctx, "registry-2", client2, &AgentCard{Name: "agent-2"}, RegistrationConfig{
			HeartbeatInterval: time.Hour,
		})
		if err != nil {
			t.Fatalf("RegisterAgent 2 failed: %v", err)
		}

		// Deregister all
		err = m.DeregisterAll(ctx)
		if err != nil {
			t.Fatalf("DeregisterAll failed: %v", err)
		}

		// Verify both clients were called
		if client1.getDeregisterCalls() != 1 {
			t.Errorf("client1: expected 1 deregister call, got %d", client1.getDeregisterCalls())
		}
		if client2.getDeregisterCalls() != 1 {
			t.Errorf("client2: expected 1 deregister call, got %d", client2.getDeregisterCalls())
		}

		// Verify no registrations remain
		if m.IsRegistered("registry-1", "agent-1") {
			t.Error("agent-1 should not be registered")
		}
		if m.IsRegistered("registry-2", "agent-2") {
			t.Error("agent-2 should not be registered")
		}
	})

	t.Run("returns error when some deregistrations fail", func(t *testing.T) {
		m := NewRegistrationManager()
		client1 := &mockRegistrationClient{}
		client2 := &mockRegistrationClient{
			deregisterErr: errors.New("deregistration failed"),
		}

		// Register multiple agents
		_ = m.RegisterAgent(ctx, "registry-1", client1, &AgentCard{Name: "agent-1"}, RegistrationConfig{
			HeartbeatInterval: time.Hour,
		})
		_ = m.RegisterAgent(ctx, "registry-2", client2, &AgentCard{Name: "agent-2"}, RegistrationConfig{
			HeartbeatInterval: time.Hour,
		})

		// Deregister all - should return error but still attempt all
		err := m.DeregisterAll(ctx)
		if err == nil {
			t.Fatal("expected error when some deregistrations fail")
		}

		// Both should have been attempted
		if client1.getDeregisterCalls() != 1 {
			t.Errorf("client1: expected 1 deregister call, got %d", client1.getDeregisterCalls())
		}
		if client2.getDeregisterCalls() != 1 {
			t.Errorf("client2: expected 1 deregister call, got %d", client2.getDeregisterCalls())
		}
	})

	t.Run("stops all heartbeat loops", func(t *testing.T) {
		m := NewRegistrationManager()
		client := &mockRegistrationClient{}

		// Register with short heartbeat
		_ = m.RegisterAgent(ctx, "test-registry", client, &AgentCard{Name: "heartbeat-agent"}, RegistrationConfig{
			HeartbeatInterval: 30 * time.Millisecond,
		})

		// Wait for heartbeats
		time.Sleep(80 * time.Millisecond)
		heartbeatsBefore := client.getHeartbeatCalls()

		// Deregister all
		_ = m.DeregisterAll(ctx)

		// Wait and verify no more heartbeats
		time.Sleep(80 * time.Millisecond)
		heartbeatsAfter := client.getHeartbeatCalls()

		if heartbeatsAfter != heartbeatsBefore {
			t.Errorf("heartbeats should stop: before=%d, after=%d", heartbeatsBefore, heartbeatsAfter)
		}
	})
}

// TestIsRegistered tests the registration status check.
// **Validates: Requirements 3.1**
func TestIsRegistered(t *testing.T) {
	ctx := context.Background()
	m := NewRegistrationManager()
	client := &mockRegistrationClient{}

	// Not registered initially
	if m.IsRegistered("test-registry", "test-agent") {
		t.Error("agent should not be registered initially")
	}

	// Register
	_ = m.RegisterAgent(ctx, "test-registry", client, &AgentCard{Name: "test-agent"}, RegistrationConfig{
		HeartbeatInterval: time.Hour,
	})

	// Should be registered
	if !m.IsRegistered("test-registry", "test-agent") {
		t.Error("agent should be registered")
	}

	// Different registry should not match
	if m.IsRegistered("other-registry", "test-agent") {
		t.Error("agent should not be registered in other registry")
	}

	// Clean up
	_ = m.DeregisterAgent(ctx, "test-registry", "test-agent")
}

// TestRegisteredAgents tests listing registered agents.
// **Validates: Requirements 3.1**
func TestRegisteredAgents(t *testing.T) {
	ctx := context.Background()
	m := NewRegistrationManager()
	client := &mockRegistrationClient{}

	// Empty initially
	agents := m.RegisteredAgents("test-registry")
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}

	// Register agents
	_ = m.RegisterAgent(ctx, "test-registry", client, &AgentCard{Name: "agent-1"}, RegistrationConfig{
		HeartbeatInterval: time.Hour,
	})
	_ = m.RegisterAgent(ctx, "test-registry", client, &AgentCard{Name: "agent-2"}, RegistrationConfig{
		HeartbeatInterval: time.Hour,
	})
	_ = m.RegisterAgent(ctx, "other-registry", client, &AgentCard{Name: "agent-3"}, RegistrationConfig{
		HeartbeatInterval: time.Hour,
	})

	// Should list only agents for specified registry
	agents = m.RegisteredAgents("test-registry")
	if len(agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(agents))
	}

	// Verify agent names
	agentSet := make(map[string]bool)
	for _, a := range agents {
		agentSet[a] = true
	}
	if !agentSet["agent-1"] || !agentSet["agent-2"] {
		t.Error("missing expected agents")
	}
	if agentSet["agent-3"] {
		t.Error("agent-3 should not be in test-registry")
	}

	// Clean up
	_ = m.DeregisterAll(ctx)
}

// TestRegistrationKey tests the registration key generation.
func TestRegistrationKey(t *testing.T) {
	key := registrationKey("my-registry", "my-agent")
	expected := "my-registry:my-agent"
	if key != expected {
		t.Errorf("registrationKey: got %q, want %q", key, expected)
	}
}

// mockRegistrationClientWithHeartbeatFunc allows custom heartbeat behavior.
type mockRegistrationClientWithHeartbeatFunc struct {
	heartbeatFunc func(ctx context.Context, agentID string) error
}

func (m *mockRegistrationClientWithHeartbeatFunc) Register(_ context.Context, _ *AgentCard) error {
	return nil
}

func (m *mockRegistrationClientWithHeartbeatFunc) Deregister(_ context.Context, _ string) error {
	return nil
}

func (m *mockRegistrationClientWithHeartbeatFunc) Heartbeat(ctx context.Context, agentID string) error {
	if m.heartbeatFunc != nil {
		return m.heartbeatFunc(ctx, agentID)
	}
	return nil
}

// testLogger is a simple logger for testing.
type testLogger struct {
	mu       sync.Mutex
	messages []string
}

func (l *testLogger) Debug(_ context.Context, msg string, _ ...any) {
	l.mu.Lock()
	l.messages = append(l.messages, "DEBUG: "+msg)
	l.mu.Unlock()
}

func (l *testLogger) Info(_ context.Context, msg string, _ ...any) {
	l.mu.Lock()
	l.messages = append(l.messages, "INFO: "+msg)
	l.mu.Unlock()
}

func (l *testLogger) Warn(_ context.Context, msg string, _ ...any) {
	l.mu.Lock()
	l.messages = append(l.messages, "WARN: "+msg)
	l.mu.Unlock()
}

func (l *testLogger) Error(_ context.Context, msg string, _ ...any) {
	l.mu.Lock()
	l.messages = append(l.messages, "ERROR: "+msg)
	l.mu.Unlock()
}
