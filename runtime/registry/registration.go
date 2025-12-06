// Package registry provides runtime components for managing MCP registry
// connections, tool discovery, and catalog synchronization.
package registry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

type (
	// RegistrationManager handles agent registration, heartbeat, and
	// deregistration with MCP registries.
	RegistrationManager struct {
		mu            sync.RWMutex
		registrations map[string]*agentRegistration
		obs           *Observability
		logger        Logger
	}

	// agentRegistration tracks a single agent's registration state.
	agentRegistration struct {
		agentID           string
		registryName      string
		client            RegistrationClient
		card              *AgentCard
		heartbeatInterval time.Duration
		heartbeatCtx      context.Context
		heartbeatCancel   context.CancelFunc
		heartbeatWg       sync.WaitGroup
	}

	// RegistrationClient defines the interface for registry registration operations.
	// Generated registry clients implement this interface.
	RegistrationClient interface {
		// Register registers an agent card with the registry.
		Register(ctx context.Context, card *AgentCard) error
		// Deregister removes an agent from the registry.
		Deregister(ctx context.Context, agentID string) error
		// Heartbeat sends a heartbeat to maintain agent registration.
		Heartbeat(ctx context.Context, agentID string) error
	}

	// AgentCard contains metadata about an agent for registration.
	// This mirrors the generated AgentCard type in registry clients.
	//
	//nolint:tagliatelle // A2A protocol uses camelCase field names
	AgentCard struct {
		// ProtocolVersion is the A2A protocol version.
		ProtocolVersion string `json:"protocolVersion"`
		// Name is the agent identifier.
		Name string `json:"name"`
		// Description explains what the agent does.
		Description string `json:"description,omitempty"`
		// URL is the agent's endpoint.
		URL string `json:"url"`
		// Version is the agent version.
		Version string `json:"version,omitempty"`
		// Capabilities lists agent capabilities.
		Capabilities map[string]any `json:"capabilities,omitempty"`
		// Skills lists the agent's skills.
		Skills []*Skill `json:"skills,omitempty"`
		// SecuritySchemes defines authentication methods.
		SecuritySchemes map[string]*SecurityScheme `json:"securitySchemes,omitempty"`
		// Security specifies which security schemes are required.
		Security []map[string][]string `json:"security,omitempty"`
	}

	// Skill represents an agent skill (tool capability).
	//
	//nolint:tagliatelle // A2A protocol uses camelCase field names
	Skill struct {
		// ID is the skill identifier.
		ID string `json:"id"`
		// Name is the human-readable name.
		Name string `json:"name"`
		// Description explains what the skill does.
		Description string `json:"description,omitempty"`
		// Tags are metadata tags.
		Tags []string `json:"tags,omitempty"`
		// InputModes specifies accepted input content types.
		InputModes []string `json:"inputModes,omitempty"`
		// OutputModes specifies output content types.
		OutputModes []string `json:"outputModes,omitempty"`
	}

	// SecurityScheme defines an authentication method.
	SecurityScheme struct {
		// Type is the security scheme type (http, apiKey, oauth2).
		Type string `json:"type"`
		// Scheme is the HTTP auth scheme (bearer, basic).
		Scheme string `json:"scheme,omitempty"`
		// In specifies where the API key is sent (header, query, cookie).
		In string `json:"in,omitempty"`
		// Name is the parameter name for API keys.
		Name string `json:"name,omitempty"`
		// Flows contains OAuth2 flow configurations.
		Flows *OAuth2Flows `json:"flows,omitempty"`
	}

	// OAuth2Flows contains OAuth2 flow configurations.
	//
	//nolint:tagliatelle // A2A protocol uses camelCase field names
	OAuth2Flows struct {
		// ClientCredentials is the client credentials flow.
		ClientCredentials *OAuth2Flow `json:"clientCredentials,omitempty"`
		// AuthorizationCode is the authorization code flow.
		AuthorizationCode *OAuth2Flow `json:"authorizationCode,omitempty"`
	}

	// OAuth2Flow contains a single OAuth2 flow configuration.
	//
	//nolint:tagliatelle // A2A protocol uses camelCase field names
	OAuth2Flow struct {
		// AuthorizationURL is the authorization endpoint.
		AuthorizationURL string `json:"authorizationUrl,omitempty"`
		// TokenURL is the token endpoint.
		TokenURL string `json:"tokenUrl,omitempty"`
		// Scopes maps scope names to descriptions.
		Scopes map[string]string `json:"scopes,omitempty"`
	}

	// Logger is the logging interface used by RegistrationManager.
	Logger interface {
		Debug(ctx context.Context, msg string, keyvals ...any)
		Info(ctx context.Context, msg string, keyvals ...any)
		Warn(ctx context.Context, msg string, keyvals ...any)
		Error(ctx context.Context, msg string, keyvals ...any)
	}

	// RegistrationOption configures a RegistrationManager.
	RegistrationOption func(*RegistrationManager)
)

// WithRegistrationLogger sets the logger for the registration manager.
func WithRegistrationLogger(l Logger) RegistrationOption {
	return func(m *RegistrationManager) {
		m.logger = l
	}
}

// WithRegistrationObservability sets the observability helper for the registration manager.
func WithRegistrationObservability(obs *Observability) RegistrationOption {
	return func(m *RegistrationManager) {
		m.obs = obs
	}
}

// NewRegistrationManager creates a new registration manager.
func NewRegistrationManager(opts ...RegistrationOption) *RegistrationManager {
	m := &RegistrationManager{
		registrations: make(map[string]*agentRegistration),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	if m.logger == nil {
		m.logger = &noopLogger{}
	}
	if m.obs == nil {
		m.obs = NewObservability(m.logger, nil, nil)
	}
	return m
}

// RegistrationConfig holds configuration for agent registration.
type RegistrationConfig struct {
	// HeartbeatInterval specifies how often to send heartbeats.
	// If zero, defaults to 30 seconds.
	HeartbeatInterval time.Duration
}

// RegisterAgent registers an agent card with a registry and starts the heartbeat loop.
// The agent will remain registered until Deregister is called or the context is cancelled.
func (m *RegistrationManager) RegisterAgent(ctx context.Context, registryName string, client RegistrationClient, card *AgentCard, cfg RegistrationConfig) error {
	start := time.Now()

	// Start trace span
	ctx, span := m.obs.StartSpan(ctx, OpRegister,
		attribute.String("registry", registryName),
		attribute.String("agent_id", card.Name),
	)

	var outcome OperationOutcome
	var opErr error
	defer func() {
		duration := time.Since(start)
		event := OperationEvent{
			Operation: OpRegister,
			Registry:  registryName,
			Duration:  duration,
			Outcome:   outcome,
		}
		if opErr != nil {
			event.Error = opErr.Error()
		}
		m.obs.LogOperation(ctx, event)
		m.obs.RecordOperationMetrics(event)
		m.obs.EndSpan(span, outcome, opErr)
	}()

	// Check if already registered
	regKey := registrationKey(registryName, card.Name)
	m.mu.RLock()
	_, exists := m.registrations[regKey]
	m.mu.RUnlock()
	if exists {
		outcome = OutcomeError
		opErr = fmt.Errorf("agent %q already registered with registry %q", card.Name, registryName)
		return opErr
	}

	// Register with the registry
	if err := client.Register(ctx, card); err != nil {
		outcome = OutcomeError
		opErr = fmt.Errorf("registering agent %q with registry %q: %w", card.Name, registryName, err)
		return opErr
	}

	// Set default heartbeat interval
	heartbeatInterval := cfg.HeartbeatInterval
	if heartbeatInterval == 0 {
		heartbeatInterval = 30 * time.Second
	}

	// Create registration entry
	heartbeatCtx, heartbeatCancel := context.WithCancel(context.Background())
	reg := &agentRegistration{
		agentID:           card.Name,
		registryName:      registryName,
		client:            client,
		card:              card,
		heartbeatInterval: heartbeatInterval,
		heartbeatCtx:      heartbeatCtx,
		heartbeatCancel:   heartbeatCancel,
	}

	// Store registration
	m.mu.Lock()
	m.registrations[regKey] = reg
	m.mu.Unlock()

	// Start heartbeat loop
	reg.heartbeatWg.Add(1)
	go m.heartbeatLoop(reg)

	m.logger.Info(ctx, "agent registered with registry",
		"registry", registryName,
		"agent_id", card.Name,
		"heartbeat_interval", heartbeatInterval.String(),
	)

	outcome = OutcomeSuccess
	return nil
}

// DeregisterAgent removes an agent from a registry and stops the heartbeat loop.
func (m *RegistrationManager) DeregisterAgent(ctx context.Context, registryName, agentID string) error {
	start := time.Now()

	// Start trace span
	ctx, span := m.obs.StartSpan(ctx, OpDeregister,
		attribute.String("registry", registryName),
		attribute.String("agent_id", agentID),
	)

	var outcome OperationOutcome
	var opErr error
	defer func() {
		duration := time.Since(start)
		event := OperationEvent{
			Operation: OpDeregister,
			Registry:  registryName,
			Duration:  duration,
			Outcome:   outcome,
		}
		if opErr != nil {
			event.Error = opErr.Error()
		}
		m.obs.LogOperation(ctx, event)
		m.obs.RecordOperationMetrics(event)
		m.obs.EndSpan(span, outcome, opErr)
	}()

	// Find and remove registration
	regKey := registrationKey(registryName, agentID)
	m.mu.Lock()
	reg, exists := m.registrations[regKey]
	if exists {
		delete(m.registrations, regKey)
	}
	m.mu.Unlock()

	if !exists {
		outcome = OutcomeError
		opErr = fmt.Errorf("agent %q not registered with registry %q", agentID, registryName)
		return opErr
	}

	// Stop heartbeat loop
	reg.heartbeatCancel()
	reg.heartbeatWg.Wait()

	// Deregister from the registry
	if err := reg.client.Deregister(ctx, agentID); err != nil {
		outcome = OutcomeError
		opErr = fmt.Errorf("deregistering agent %q from registry %q: %w", agentID, registryName, err)
		return opErr
	}

	m.logger.Info(ctx, "agent deregistered from registry",
		"registry", registryName,
		"agent_id", agentID,
	)

	outcome = OutcomeSuccess
	return nil
}

// DeregisterAll deregisters all agents from all registries.
// This should be called during graceful shutdown.
func (m *RegistrationManager) DeregisterAll(ctx context.Context) error {
	m.mu.Lock()
	regs := make([]*agentRegistration, 0, len(m.registrations))
	for _, reg := range m.registrations {
		regs = append(regs, reg)
	}
	m.registrations = make(map[string]*agentRegistration)
	m.mu.Unlock()

	var errs []error
	for _, reg := range regs {
		// Stop heartbeat loop
		reg.heartbeatCancel()
		reg.heartbeatWg.Wait()

		// Deregister from the registry
		if err := reg.client.Deregister(ctx, reg.agentID); err != nil {
			m.logger.Error(ctx, "failed to deregister agent",
				"registry", reg.registryName,
				"agent_id", reg.agentID,
				"error", err,
			)
			errs = append(errs, fmt.Errorf("deregistering agent %q from registry %q: %w", reg.agentID, reg.registryName, err))
		} else {
			m.logger.Info(ctx, "agent deregistered from registry",
				"registry", reg.registryName,
				"agent_id", reg.agentID,
			)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to deregister %d agents: %v", len(errs), errs)
	}
	return nil
}

// heartbeatLoop sends periodic heartbeats to maintain agent registration.
func (m *RegistrationManager) heartbeatLoop(reg *agentRegistration) {
	defer reg.heartbeatWg.Done()

	ticker := time.NewTicker(reg.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-reg.heartbeatCtx.Done():
			return
		case <-ticker.C:
			m.sendHeartbeat(reg)
		}
	}
}

// sendHeartbeat sends a single heartbeat to the registry.
func (m *RegistrationManager) sendHeartbeat(reg *agentRegistration) {
	ctx := reg.heartbeatCtx
	start := time.Now()

	// Start trace span
	ctx, span := m.obs.StartSpan(ctx, OpHeartbeat,
		attribute.String("registry", reg.registryName),
		attribute.String("agent_id", reg.agentID),
	)

	var outcome OperationOutcome
	var opErr error
	defer func() {
		duration := time.Since(start)
		event := OperationEvent{
			Operation: OpHeartbeat,
			Registry:  reg.registryName,
			Duration:  duration,
			Outcome:   outcome,
		}
		if opErr != nil {
			event.Error = opErr.Error()
		}
		m.obs.LogOperation(ctx, event)
		m.obs.RecordOperationMetrics(event)
		m.obs.EndSpan(span, outcome, opErr)
	}()

	if err := reg.client.Heartbeat(ctx, reg.agentID); err != nil {
		outcome = OutcomeError
		opErr = err
		m.logger.Warn(ctx, "heartbeat failed",
			"registry", reg.registryName,
			"agent_id", reg.agentID,
			"error", err,
		)
		return
	}

	outcome = OutcomeSuccess
	m.logger.Debug(ctx, "heartbeat sent",
		"registry", reg.registryName,
		"agent_id", reg.agentID,
	)
}

// IsRegistered returns true if the agent is registered with the specified registry.
func (m *RegistrationManager) IsRegistered(registryName, agentID string) bool {
	regKey := registrationKey(registryName, agentID)
	m.mu.RLock()
	_, exists := m.registrations[regKey]
	m.mu.RUnlock()
	return exists
}

// RegisteredAgents returns a list of all registered agent IDs for a registry.
func (m *RegistrationManager) RegisteredAgents(registryName string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var agents []string
	for _, reg := range m.registrations {
		if reg.registryName == registryName {
			agents = append(agents, reg.agentID)
		}
	}
	return agents
}

// registrationKey generates a unique key for a registration.
func registrationKey(registryName, agentID string) string {
	return registryName + ":" + agentID
}
