// Package registry provides the internal tool registry service implementation.
//
// This file owns the transport-facing registry contract: it admits toolsets
// into the shared catalog, validates routed tool calls against admitted
// schemas, gates execution on provider health, and publishes tool-call traffic
// onto the canonical registry streams.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry"
	streamopts "goa.design/pulse/streaming/options"
)

type (
	// Service implements the registry service interface.
	// It provides toolset registration, discovery, and tool invocation capabilities.
	Service struct {
		catalog        *toolsetCatalog
		validator      *schemaValidator
		streamManager  StreamManager
		healthTracker  HealthTracker
		callAdmissions *callAdmissionStore

		pulseClient           clientspulse.Client
		resultStreamTTL       time.Duration
		providerLeaseDuration time.Duration
	}

	// serviceOptions configures the registry service.
	serviceOptions struct {
		// catalog is the authoritative toolset catalog.
		catalog *toolsetCatalog
		// StreamManager manages Pulse streams for toolset communication.
		StreamManager StreamManager
		// HealthTracker tracks provider health status.
		HealthTracker HealthTracker
		// CallAdmissions atomically coordinates call publication across replicas.
		CallAdmissions *callAdmissionStore
		// PulseClient creates/opens Pulse streams. Required for CallTool.
		PulseClient clientspulse.Client
		// ResultStreamTTL controls how long result streams live in Redis.
		// When zero, defaults to toolregistry.DefaultResultStreamTTL.
		ResultStreamTTL time.Duration
		// ProviderLeaseDuration is the application-level provider membership
		// lifetime renewed by identical registration.
		ProviderLeaseDuration time.Duration
	}
)

// DefaultProviderLeaseDuration is the application-level provider membership
// lifetime when the registry Config does not specify one.
const DefaultProviderLeaseDuration = 2 * time.Minute

// Compile-time check that Service implements the generated interface.
var _ genregistry.Service = (*Service)(nil)

// newService wires the registry service over the already-constructed catalog,
// stream manager, health tracker, and Pulse client.
func newService(opts serviceOptions) (*Service, error) {
	if opts.catalog == nil {
		return nil, fmt.Errorf("toolset catalog is required")
	}
	if opts.StreamManager == nil {
		return nil, fmt.Errorf("stream manager is required")
	}
	if opts.HealthTracker == nil {
		return nil, fmt.Errorf("health tracker is required")
	}
	if opts.CallAdmissions == nil {
		return nil, fmt.Errorf("call admission store is required")
	}
	if opts.PulseClient == nil {
		return nil, fmt.Errorf("pulse client is required")
	}
	if opts.ProviderLeaseDuration < 0 {
		return nil, fmt.Errorf("provider lease duration must not be negative")
	}
	ttl := opts.ResultStreamTTL
	if ttl == 0 {
		ttl = toolregistry.DefaultResultStreamTTL
	}
	if ttl < toolregistry.MinResultStreamTTL || ttl > toolregistry.MaxResultStreamTTL {
		return nil, fmt.Errorf(
			"result stream TTL must be between %s and %s",
			toolregistry.MinResultStreamTTL,
			toolregistry.MaxResultStreamTTL,
		)
	}
	providerLeaseDuration := opts.ProviderLeaseDuration
	if providerLeaseDuration == 0 {
		providerLeaseDuration = DefaultProviderLeaseDuration
	}
	if providerLeaseDuration < toolregistry.MinProviderLeaseDuration {
		return nil, fmt.Errorf(
			"provider lease duration must be at least %s",
			toolregistry.MinProviderLeaseDuration,
		)
	}
	if providerLeaseDuration > toolregistry.MaxProviderLeaseDuration {
		return nil, fmt.Errorf(
			"provider lease duration must not exceed %s",
			toolregistry.MaxProviderLeaseDuration,
		)
	}
	return &Service{
		catalog:               opts.catalog,
		validator:             newSchemaValidator(),
		streamManager:         opts.StreamManager,
		healthTracker:         opts.HealthTracker,
		callAdmissions:        opts.CallAdmissions,
		pulseClient:           opts.PulseClient,
		resultStreamTTL:       ttl,
		providerLeaseDuration: providerLeaseDuration,
	}, nil
}

// Register prepares routing and atomically creates, renews, or replaces the
// catalog-owned admission and provider lease.
func (s *Service) Register(ctx context.Context, p *genregistry.RegisterPayload) (*genregistry.RegisterResult, error) {
	// Validate tool schemas.
	if err := s.validator.ValidateToolSchemas(p.Tools); err != nil {
		return nil, genregistry.MakeValidationError(fmt.Errorf("invalid tool schema: %w", err))
	}

	// Ensure the Pulse request stream for this toolset exists.
	_, _, err := s.streamManager.GetOrCreateStream(ctx, p.Name)
	if err != nil {
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf("create stream for toolset: %w", err))
	}

	toolset := &genregistry.Toolset{
		Name:        p.Name,
		Description: p.Description,
		Version:     p.Version,
		Tags:        p.Tags,
		Tools:       p.Tools,
	}

	admission, err := s.catalog.Register(
		ctx,
		toolset,
		p.AdmissionRevision,
		p.ProviderID,
		p.ProviderIncarnationID,
		s.providerLeaseDuration,
	)
	if err != nil {
		switch {
		case errors.Is(err, errAdmissionBlocked):
			return nil, genregistry.MakeAdmissionBlocked(err)
		case errors.Is(err, errAdmissionRetired):
			return nil, genregistry.MakeAdmissionRetired(err)
		default:
			return nil, genregistry.MakeServiceUnavailable(fmt.Errorf("register toolset admission: %w", err))
		}
	}
	if err := s.healthTracker.EnsurePingLoop(ctx, p.Name); err != nil {
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf("ensure health ping loop: %w", err))
	}

	return &genregistry.RegisterResult{
		RegisteredAt:      admission.RegisteredAt,
		RegistrationToken: admission.RegistrationToken,
		LeaseDurationMs:   s.providerLeaseDuration.Milliseconds(),
	}, nil
}

// ReleaseProvider removes one exact provider lease after its Serve lifecycle
// has stopped claiming and settled work.
func (s *Service) ReleaseProvider(ctx context.Context, p *genregistry.ReleaseProviderPayload) error {
	if err := s.catalog.ReleaseProvider(
		ctx,
		p.Name,
		p.ProviderID,
		p.ProviderIncarnationID,
		p.ExpectedRegistrationToken,
	); err != nil {
		return genregistry.MakeServiceUnavailable(fmt.Errorf("release provider lease: %w", err))
	}
	return nil
}

// Unregister intentionally retires exactly the expected admission.
func (s *Service) Unregister(ctx context.Context, p *genregistry.UnregisterPayload) error {
	err := s.catalog.Retire(ctx, p.Name, p.ExpectedRegistrationToken)
	if err != nil {
		switch {
		case errors.Is(err, errAdmissionConflict):
			return genregistry.MakeAdmissionConflict(err)
		default:
			return genregistry.MakeServiceUnavailable(fmt.Errorf("retire toolset admission: %w", err))
		}
	}
	return nil
}

// Pong records generation-level consumer-group liveness only when the
// responding provider has an unexpired application lease.
func (s *Service) Pong(ctx context.Context, p *genregistry.PongPayload) error {
	return s.healthTracker.RecordPong(
		ctx,
		p.Toolset,
		p.ProviderID,
		p.ProviderIncarnationID,
		p.PingID,
	)
}

// ListToolsets returns all registered toolsets with optional tag filtering.
// Returns all toolsets with metadata, supports tag filtering, and returns
// an empty list when the catalog is empty.
// **Validates: Requirements 6.1, 6.2, 6.3**
func (s *Service) ListToolsets(ctx context.Context, p *genregistry.ListToolsetsPayload) (*genregistry.ListToolsetsResult, error) {
	toolsets, err := s.catalog.ListToolsets(ctx, p.Tags)
	if err != nil {
		return nil, fmt.Errorf("list toolsets: %w", err)
	}

	infos := make([]*genregistry.ToolsetInfo, len(toolsets))
	for i, ts := range toolsets {
		infos[i] = toolsetToInfo(ts)
	}

	return &genregistry.ListToolsetsResult{
		Toolsets: infos,
	}, nil
}

// toolsetToInfo converts a Toolset to ToolsetInfo (metadata without full tool schemas).
func toolsetToInfo(ts *genregistry.Toolset) *genregistry.ToolsetInfo {
	return &genregistry.ToolsetInfo{
		Name:         ts.Name,
		Description:  ts.Description,
		Version:      ts.Version,
		Tags:         ts.Tags,
		ToolCount:    len(ts.Tools),
		RegisteredAt: ts.RegisteredAt,
	}
}

// GetToolset returns a specific toolset by name including all tool schemas.
// Returns the complete toolset with tool schemas, or not-found error if
// the toolset doesn't exist.
// **Validates: Requirements 7.1, 7.2**
func (s *Service) GetToolset(ctx context.Context, p *genregistry.GetToolsetPayload) (*genregistry.Toolset, error) {
	toolset, err := s.catalog.GetToolset(ctx, p.Name)
	if err != nil {
		if errors.Is(err, errToolsetNotFound) {
			return nil, genregistry.MakeNotFound(fmt.Errorf("toolset %q not found", p.Name))
		}
		return nil, fmt.Errorf("get toolset: %w", err)
	}
	return toolset, nil
}

// Search searches toolsets by keyword matching name, description, or tags.
// Returns matching toolsets or an empty list when no matches are found.
// **Validates: Requirements 8.1, 8.2**
func (s *Service) Search(ctx context.Context, p *genregistry.SearchPayload) (*genregistry.SearchResult, error) {
	toolsets, err := s.catalog.SearchToolsets(ctx, p.Query)
	if err != nil {
		return nil, fmt.Errorf("search toolsets: %w", err)
	}

	infos := make([]*genregistry.ToolsetInfo, len(toolsets))
	for i, ts := range toolsets {
		infos[i] = toolsetToInfo(ts)
	}

	return &genregistry.SearchResult{
		Toolsets: infos,
	}, nil
}

// CallTool invokes a tool through the registry gateway.
// It validates the payload against the tool's payload schema, checks provider health,
// creates the per-call result stream, and publishes the request to the toolset stream.
func (s *Service) CallTool(ctx context.Context, p *genregistry.CallToolPayload) (*genregistry.CallToolResult, error) {
	// 1. Load the exact catalog generation used for validation and routing.
	registration, err := s.catalog.ActiveRegistration(ctx, p.Toolset)
	if err != nil {
		if errors.Is(err, errToolsetNotFound) {
			return nil, genregistry.MakeNotFound(fmt.Errorf("toolset %q not found", p.Toolset))
		}
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf("get toolset: %w", err))
	}

	toolset := registration.Toolset

	// 2. Find the tool within the toolset.
	var tool *genregistry.ToolSchema
	for _, t := range toolset.Tools {
		if t.Name == p.Tool {
			tool = t
			break
		}
	}
	if tool == nil {
		return nil, genregistry.MakeNotFound(fmt.Errorf("tool %q not found in toolset %q", p.Tool, p.Toolset))
	}

	// 3. Validate payload against tool's payload schema.
	if err := s.validator.ValidatePayload(tool.PayloadSchema, p.PayloadJSON); err != nil {
		return nil, genregistry.MakeValidationError(fmt.Errorf("payload validation failed: %w", err))
	}

	// 4. Check provider health - return service_unavailable if unhealthy.
	h, err := s.healthTracker.Health(ctx, p.Toolset, registration.RegistrationToken)
	if err != nil {
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf("check toolset %q health: %w", p.Toolset, err))
	}
	if !h.Healthy {
		lastPong := "missing"
		if !h.LastPong.IsZero() {
			lastPong = h.LastPong.UTC().Format(time.RFC3339Nano)
		}
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf(
			"no healthy providers for toolset %q (staleness_threshold=%s, last_pong=%s, age=%s)",
			p.Toolset,
			h.StalenessThreshold,
			lastPong,
			h.Age,
		))
	}

	toolUseID := toolUseIDForCall(p.Meta)
	resultStreamID := toolregistry.ResultStreamID(toolUseID)
	resultStream, err := s.pulseClient.Stream(
		resultStreamID,
		streamopts.WithStreamSlidingTTL(s.resultStreamTTL),
	)
	if err != nil {
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf("open result stream %q: %w", resultStreamID, err))
	}

	meta := toolregistry.ToolCallMeta{
		RunID:            p.Meta.RunID,
		SessionID:        p.Meta.SessionID,
		TurnID:           derefString(p.Meta.TurnID),
		ToolCallID:       p.Meta.ToolCallID,
		ParentToolCallID: derefString(p.Meta.ParentToolCallID),
	}
	msg := toolregistry.NewToolCallMessage(
		registration.RegistrationToken,
		toolUseID,
		s.resultStreamTTL,
		tools.Ident(p.Tool),
		json.RawMessage(p.PayloadJSON),
		&meta,
	)
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf("encode call admission: %w", err))
	}
	digest := sha256.Sum256(body)
	admission, created, err := s.callAdmissions.Ensure(
		ctx,
		toolUseID,
		registration.RegistrationToken,
		fmt.Sprintf("%x", digest[:]),
		s.resultStreamTTL,
	)
	if err != nil {
		if errors.Is(err, errCallAdmissionConflict) {
			return nil, genregistry.MakeValidationError(err)
		}
		return nil, genregistry.MakeServiceUnavailable(err)
	}

	var overloadEventID string
	if !created {
		eventID, result, err := latestExactResult(
			ctx,
			resultStream,
			toolUseID,
			registration.RegistrationToken,
		)
		if err != nil {
			return nil, genregistry.MakeServiceUnavailable(fmt.Errorf("inspect admitted call history: %w", err))
		}
		if result != nil {
			if result.Error == nil || result.Error.Code != toolregistry.ToolErrorCodeProviderOverloaded {
				return callToolResult(toolUseID, registration.RegistrationToken, s.resultStreamTTL), nil
			}
			if result.Error.RetryAfterMillis <= 0 ||
				result.Error.RetryAfterMillis > toolregistry.MaxProviderOverloadRetryAfter.Milliseconds() {
				return nil, genregistry.MakeServiceUnavailable(fmt.Errorf(
					"provider overload retry delay %dms is outside (0,%dms]",
					result.Error.RetryAfterMillis,
					toolregistry.MaxProviderOverloadRetryAfter.Milliseconds(),
				))
			}
			overloadEventID = eventID
		}
	}

	publication, claimed, err := s.callAdmissions.ClaimPublication(ctx, admission, overloadEventID)
	if err != nil {
		return nil, genregistry.MakeServiceUnavailable(err)
	}
	if claimed {
		publishErr := s.publishAdmittedCall(
			ctx,
			p,
			registration.RegistrationToken,
			resultStream,
			resultStreamID,
			msg,
			admission,
			publication,
			overloadEventID,
		)
		releaseCtx, releaseCancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			toolregistry.ResultStreamTransportBudget,
		)
		releaseErr := publication.Release(releaseCtx)
		releaseCancel()
		if err := errors.Join(publishErr, releaseErr); err != nil {
			return nil, genregistry.MakeServiceUnavailable(err)
		}
	}
	return callToolResult(toolUseID, registration.RegistrationToken, s.resultStreamTTL), nil
}

// publishAdmittedCall performs one exact-owned initial or overload publication.
func (s *Service) publishAdmittedCall(
	ctx context.Context,
	payload *genregistry.CallToolPayload,
	registrationToken string,
	resultStream clientspulse.Stream,
	resultStreamID string,
	message toolregistry.ToolCallMessage,
	admission callAdmission,
	publication *callPublication,
	overloadEventID string,
) error {
	if overloadEventID != "" {
		_, result, err := latestExactResult(ctx, resultStream, message.ToolUseID, registrationToken)
		if err != nil {
			return fmt.Errorf("recheck overload history: %w", err)
		}
		if result != nil && result.Error != nil {
			delay := time.Duration(result.Error.RetryAfterMillis) * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	if _, err := resultStream.Add(ctx, "init", []byte("{}")); err != nil {
		return fmt.Errorf("initialize result stream %q: %w", resultStreamID, err)
	}
	if err := s.catalog.VerifyActiveToken(ctx, payload.Toolset, registrationToken); err != nil {
		return fmt.Errorf("verify routed registration: %w", err)
	}
	if err := s.streamManager.PublishToolCall(ctx, payload.Toolset, message); err != nil {
		return fmt.Errorf("publish tool call: %w", err)
	}
	if err := publication.MarkPublished(ctx, admission, overloadEventID, s.resultStreamTTL); err != nil {
		return err
	}
	return nil
}

// toolUseIDForCall returns the stable transport identity for a registry-routed
// tool execution. A model/provider ToolCallID is scoped by RunID before hashing
// so retries reuse one result stream while concurrent runs cannot collide.
func toolUseIDForCall(meta *genregistry.ToolCallMeta) string {
	return toolregistry.DeriveToolUseID(meta.RunID, meta.ToolCallID)
}

// callToolResult returns the stable replay contract for one admitted call.
func callToolResult(toolUseID, registrationToken string, ttl time.Duration) *genregistry.CallToolResult {
	return &genregistry.CallToolResult{
		ToolUseID:         toolUseID,
		RegistrationToken: registrationToken,
		ResultStreamTTLMs: ttl.Milliseconds(),
	}
}

// latestExactResult replays currently retained history and returns the newest
// terminal event for one exact call/token without creating acknowledgement or
// consumer-group state.
func latestExactResult(
	ctx context.Context,
	stream clientspulse.Stream,
	toolUseID, registrationToken string,
) (string, *toolregistry.ToolResultMessage, error) {
	reader, err := stream.NewReader(
		ctx,
		streamopts.WithReaderStartAtOldest(),
		streamopts.WithReaderBlockDuration(10*time.Millisecond),
	)
	if err != nil {
		return "", nil, err
	}
	defer reader.Close()
	events := reader.Subscribe()
	idle := time.NewTimer(20 * time.Millisecond)
	defer idle.Stop()
	var (
		latestID string
		latest   *toolregistry.ToolResultMessage
	)
	for {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-idle.C:
			return latestID, latest, nil
		case event, ok := <-events:
			if !ok {
				return latestID, latest, nil
			}
			if event.EventName != toolregistry.ResultEventKey {
				continue
			}
			var message toolregistry.ToolResultMessage
			if err := json.Unmarshal(event.Payload, &message); err != nil {
				continue
			}
			if message.ToolUseID != toolUseID || message.RegistrationToken != registrationToken {
				continue
			}
			latestID = event.ID
			latest = &message
			if !idle.Stop() {
				<-idle.C
			}
			idle.Reset(20 * time.Millisecond)
		}
	}
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
