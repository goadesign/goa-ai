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
	goa "goa.design/goa/v3/pkg"
	streamopts "goa.design/pulse/streaming/options"
)

type (
	// callAdmissionRepository owns immutable call identity and terminal state.
	// Production uses Redis; service tests may substitute an in-memory recorder.
	callAdmissionRepository interface {
		Ensure(
			ctx context.Context,
			toolset, toolUseID, registrationToken, digest string,
			executionTimeout, ttl time.Duration,
			outcomeUnknownPayload []byte,
		) (callAdmission, bool, error)
		Reject(
			ctx context.Context,
			toolset, toolUseID, digest string,
			rejection callRejection,
			ttl time.Duration,
		) (callAdmission, error)
		Attach(ctx context.Context, toolset, toolUseID, digest string) (callAdmission, error)
		InitializeResultStream(ctx context.Context, admission callAdmission, resultStreamID string) error
		RestoreTerminal(ctx context.Context, admission callAdmission, resultStreamID string) error
		SettleLostClaimsForLease(
			ctx context.Context,
			providerRegistrationToken, providerLease string,
		) error
		Complete(
			ctx context.Context,
			toolset, toolUseID, callRegistrationToken, providerRegistrationToken,
			providerLease, requestEventID, resultStreamID string,
			payload []byte,
		) error
		PublishLiveEvent(
			ctx context.Context,
			toolset, toolUseID, callRegistrationToken, providerRegistrationToken,
			providerLease, requestEventID, resultStreamID, eventName string,
			payload []byte,
		) error
		ReportOverload(
			ctx context.Context,
			toolset, toolUseID, callRegistrationToken, providerRegistrationToken,
			providerLease, requestEventID, resultStreamID string,
			overloadPayload, stalePayload []byte,
		) error
		Claim(
			ctx context.Context,
			toolset, toolUseID, callRegistrationToken, providerRegistrationToken,
			providerLease, requestEventID, resultStreamID string,
			stalePayload []byte,
		) (callClaimDisposition, error)
	}

	// Service implements the registry service interface.
	// It provides toolset registration, discovery, and tool invocation capabilities.
	Service struct {
		catalog        *toolsetCatalog
		validator      *schemaValidator
		streamManager  StreamManager
		healthTracker  HealthTracker
		callAdmissions callAdmissionRepository

		pulseClient           clientspulse.Client
		executionTimeout      time.Duration
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
		CallAdmissions callAdmissionRepository
		// PulseClient creates/opens Pulse streams. Required for CallTool.
		PulseClient clientspulse.Client
		// ResultStreamTTL selects the retention used to derive each call record's
		// Redis-owned absolute expiration. When zero, it defaults to
		// toolregistry.DefaultResultStreamTTL.
		ResultStreamTTL time.Duration
		// ExecutionTimeout selects the duration used to derive the absolute
		// execution deadline. Zero uses toolregistry.MaxToolCallWait.
		ExecutionTimeout time.Duration
		// ProviderLeaseDuration is the application-level provider membership
		// lifetime renewed by identical registration.
		ProviderLeaseDuration time.Duration
	}

	// preparedToolCall is the immutable registry-owned request derived before
	// initial admission or exact retry touches publication state.
	preparedToolCall struct {
		toolset           string
		registrationToken string
		toolUseID         string
		resultStreamID    string
		tool              tools.Ident
		payload           json.RawMessage
		meta              *toolregistry.ToolCallMeta
		admissionDigest   string
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
	executionTimeout := opts.ExecutionTimeout
	if executionTimeout == 0 {
		executionTimeout = toolregistry.MaxToolCallWait
	}
	if executionTimeout < time.Millisecond || executionTimeout > toolregistry.MaxToolCallWait {
		return nil, fmt.Errorf(
			"tool execution timeout must be between %s and %s",
			time.Millisecond,
			toolregistry.MaxToolCallWait,
		)
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
		executionTimeout:      executionTimeout,
		resultStreamTTL:       ttl,
		providerLeaseDuration: providerLeaseDuration,
	}, nil
}

// Register prepares routing and atomically creates, renews, or replaces the
// catalog-owned admission and provider lease.
func (s *Service) Register(ctx context.Context, p *genregistry.RegisterPayload) (*genregistry.RegisterResult, error) {
	if err := toolregistry.ValidateWireProtocolVersion(p.WireProtocolVersion); err != nil {
		return nil, genregistry.MakeValidationError(err)
	}
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
	if err := s.callAdmissions.SettleLostClaimsForLease(
		ctx,
		p.ExpectedRegistrationToken,
		providerLeaseKey(p.ProviderID, p.ProviderIncarnationID),
	); err != nil {
		return genregistry.MakeServiceUnavailable(fmt.Errorf("settle released provider claims: %w", err))
	}
	return nil
}

// DrainProvider marks one exact provider lease non-routable before its request
// sink closes while preserving authority to settle already-claimed work.
func (s *Service) DrainProvider(ctx context.Context, p *genregistry.DrainProviderPayload) error {
	if err := s.catalog.DrainProvider(
		ctx,
		p.Name,
		p.ProviderID,
		p.ProviderIncarnationID,
		p.ExpectedRegistrationToken,
		time.Duration(p.SettlementDurationMs)*time.Millisecond,
	); err != nil {
		return genregistry.MakeServiceUnavailable(fmt.Errorf("drain provider lease: %w", err))
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
	if err := toolregistry.ValidateWireProtocolVersion(p.WireProtocolVersion); err != nil {
		return nil, genregistry.MakeValidationError(err)
	}
	prepared, err := prepareToolCallIdentity(
		p.Toolset,
		p.Tool,
		p.PayloadJSON,
		p.Meta,
	)
	if err != nil {
		return nil, err
	}
	admission, err := s.callAdmissions.Attach(
		ctx,
		prepared.toolset,
		prepared.toolUseID,
		prepared.admissionDigest,
	)
	if err == nil {
		if admission.terminal || admission.published {
			return s.replayCallToolResult(ctx, prepared.toolUseID, prepared.resultStreamID, admission)
		}
		return s.resumeUnpublishedToolCall(ctx, prepared, admission)
	}
	if !errors.Is(err, errCallAdmissionNotFound) {
		return nil, callDecisionError(err)
	}

	registration, err := s.activeRegistration(ctx, p.Toolset)
	if err != nil {
		return s.rejectPreparedToolCall(ctx, prepared, err)
	}
	if err := s.validatePreparedToolCall(ctx, prepared, registration); err != nil {
		return s.rejectPreparedToolCall(ctx, prepared, err)
	}
	admission, _, err = s.callAdmissions.Ensure(
		ctx,
		prepared.toolset,
		prepared.toolUseID,
		registration.RegistrationToken,
		prepared.admissionDigest,
		s.executionTimeout,
		s.resultStreamTTL,
		outcomeUnknownPayload(registration.RegistrationToken, prepared.toolUseID),
	)
	if err != nil {
		return nil, callDecisionError(err)
	}
	prepared.registrationToken = admission.registrationToken
	if admission.terminal || admission.published {
		return s.replayCallToolResult(ctx, prepared.toolUseID, prepared.resultStreamID, admission)
	}
	if admission.registrationToken != registration.RegistrationToken {
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf(
			"retained unpublished call belongs to inactive registration %q",
			admission.registrationToken,
		))
	}
	return s.routeInitialToolCall(ctx, prepared, admission)
}

// RetryTool republishes only the exact original admission after a provider
// reports overload. A replacement admission is never eligible for this retry.
func (s *Service) RetryTool(ctx context.Context, p *genregistry.RetryToolPayload) (*genregistry.CallToolResult, error) {
	if err := toolregistry.ValidateWireProtocolVersion(p.WireProtocolVersion); err != nil {
		return nil, genregistry.MakeValidationError(err)
	}
	prepared, err := prepareToolCallIdentity(
		p.Toolset,
		p.Tool,
		p.PayloadJSON,
		p.Meta,
	)
	if err != nil {
		return nil, err
	}
	admission, err := s.callAdmissions.Attach(
		ctx,
		prepared.toolset,
		prepared.toolUseID,
		prepared.admissionDigest,
	)
	if err != nil {
		switch {
		case errors.Is(err, errCallAdmissionConflict):
			return nil, genregistry.MakeValidationError(err)
		case errors.Is(err, errCallAdmissionNotFound):
			return nil, genregistry.MakeServiceUnavailable(fmt.Errorf("retry admitted tool call: %w", err))
		default:
			return nil, genregistry.MakeServiceUnavailable(err)
		}
	}
	if admission.registrationToken != p.ExpectedRegistrationToken {
		return nil, genregistry.MakeAdmissionConflict(fmt.Errorf(
			"toolset %q retained registration %s does not match retry admission %s",
			p.Toolset,
			admission.registrationToken,
			p.ExpectedRegistrationToken,
		))
	}
	prepared.registrationToken = admission.registrationToken
	if admission.terminal {
		return s.replayCallToolResult(ctx, prepared.toolUseID, prepared.resultStreamID, admission)
	}

	registration, err := s.activeRegistration(ctx, p.Toolset)
	if err != nil {
		return s.retryTerminalOrError(ctx, prepared, err)
	}
	if registration.RegistrationToken != admission.registrationToken {
		return s.retryTerminalOrError(ctx, prepared, genregistry.MakeAdmissionConflict(fmt.Errorf(
			"toolset %q active registration %s does not match retry admission %s",
			p.Toolset,
			registration.RegistrationToken,
			admission.registrationToken,
		)))
	}
	if err := s.validatePreparedToolCall(ctx, prepared, registration); err != nil {
		return s.retryTerminalOrError(ctx, prepared, err)
	}
	return s.retryPreparedToolCall(ctx, prepared, admission)
}

// retryTerminalOrError resolves a routing failure against the call record one
// final time. A terminal committed after the first Attach is authoritative and
// must replay even if its admission retired concurrently.
func (s *Service) retryTerminalOrError(
	ctx context.Context,
	prepared preparedToolCall,
	routingErr error,
) (*genregistry.CallToolResult, error) {
	admission, err := s.callAdmissions.Attach(
		ctx,
		prepared.toolset,
		prepared.toolUseID,
		prepared.admissionDigest,
	)
	if err != nil || !admission.terminal {
		return nil, routingErr
	}
	return s.replayCallToolResult(ctx, prepared.toolUseID, prepared.resultStreamID, admission)
}

// CompleteToolCall atomically commits one exact provider terminal result.
func (s *Service) CompleteToolCall(ctx context.Context, p *genregistry.CompleteToolCallPayload) error {
	var result toolregistry.ToolResultMessage
	if err := json.Unmarshal(p.ResultJSON, &result); err != nil {
		return genregistry.MakeValidationError(fmt.Errorf("decode terminal result: %w", err))
	}
	if err := toolregistry.ValidateToolResultMessage(result); err != nil {
		return genregistry.MakeValidationError(fmt.Errorf("validate terminal result: %w", err))
	}
	if result.Retry != nil {
		return genregistry.MakeValidationError(fmt.Errorf("terminal result must not contain retry control"))
	}
	if result.ToolUseID != p.ToolUseID || result.RegistrationToken != p.RegistrationToken {
		return genregistry.MakeValidationError(fmt.Errorf("terminal result identity does not match payload"))
	}
	if err := s.callAdmissions.Complete(
		ctx,
		p.Toolset,
		p.ToolUseID,
		p.RegistrationToken,
		p.ProviderRegistrationToken,
		providerLeaseKey(p.ProviderID, p.ProviderIncarnationID),
		p.RequestEventID,
		toolregistry.ResultStreamID(p.ToolUseID),
		p.ResultJSON,
	); err != nil {
		if errors.Is(err, errCallTerminalConflict) {
			return genregistry.MakeValidationError(err)
		}
		return genregistry.MakeServiceUnavailable(fmt.Errorf("complete tool call: %w", err))
	}
	return nil
}

// PublishToolOutputDelta appends a provider output fragment only while its
// claimed call remains live and nonterminal.
func (s *Service) PublishToolOutputDelta(ctx context.Context, p *genregistry.PublishToolOutputDeltaPayload) error {
	if len(p.Delta) > toolregistry.MaxToolOutputDeltaBytes {
		return genregistry.MakeValidationError(fmt.Errorf(
			"output delta exceeds %d bytes",
			toolregistry.MaxToolOutputDeltaBytes,
		))
	}
	delta := toolregistry.NewToolOutputDeltaMessage(
		p.CallRegistrationToken,
		p.ToolUseID,
		p.Stream,
		p.Delta,
	)
	payload, err := json.Marshal(delta)
	if err != nil {
		return genregistry.MakeServiceUnavailable(fmt.Errorf("encode tool output delta: %w", err))
	}
	return s.publishLiveProviderEvent(
		ctx,
		p.Toolset,
		p.ProviderID,
		p.ProviderIncarnationID,
		p.ProviderRegistrationToken,
		p.CallRegistrationToken,
		p.ToolUseID,
		p.RequestEventID,
		toolregistry.OutputDeltaEventKey,
		payload,
	)
}

// ReportToolCallOverload appends canonical retry control only before dispatch.
// Stale generations receive their canonical terminal result instead.
func (s *Service) ReportToolCallOverload(ctx context.Context, p *genregistry.ProviderToolCallClaimPayload) error {
	overload := toolregistry.NewToolResultRetryMessage(
		p.CallRegistrationToken,
		p.ToolUseID,
		toolregistry.ToolRetryReasonProviderOverloaded,
		toolregistry.ProviderOverloadRetryAfter,
	)
	overloadPayload, err := json.Marshal(overload)
	if err != nil {
		return genregistry.MakeServiceUnavailable(fmt.Errorf("encode overload control: %w", err))
	}
	stale := toolregistry.NewToolResultErrorMessage(
		p.CallRegistrationToken,
		p.ToolUseID,
		toolregistry.ToolErrorCodeStaleRegistration,
		"queued tool call belongs to an older registration",
	)
	stalePayload, err := json.Marshal(stale)
	if err != nil {
		return genregistry.MakeServiceUnavailable(fmt.Errorf("encode stale terminal result: %w", err))
	}
	if err := s.callAdmissions.ReportOverload(
		ctx,
		p.Toolset,
		p.ToolUseID,
		p.CallRegistrationToken,
		p.ProviderRegistrationToken,
		providerLeaseKey(p.ProviderID, p.ProviderIncarnationID),
		p.RequestEventID,
		toolregistry.ResultStreamID(p.ToolUseID),
		overloadPayload,
		stalePayload,
	); err != nil {
		return genregistry.MakeServiceUnavailable(fmt.Errorf("report tool call overload: %w", err))
	}
	return nil
}

// ClaimToolCall performs the registry-owned pre-dispatch transition.
func (s *Service) ClaimToolCall(
	ctx context.Context,
	p *genregistry.ProviderToolCallClaimPayload,
) (*genregistry.ClaimToolCallResult, error) {
	stale := toolregistry.NewToolResultErrorMessage(
		p.CallRegistrationToken,
		p.ToolUseID,
		toolregistry.ToolErrorCodeStaleRegistration,
		"queued tool call belongs to an older registration",
	)
	stalePayload, err := json.Marshal(stale)
	if err != nil {
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf("encode stale terminal result: %w", err))
	}
	disposition, err := s.callAdmissions.Claim(
		ctx,
		p.Toolset,
		p.ToolUseID,
		p.CallRegistrationToken,
		p.ProviderRegistrationToken,
		providerLeaseKey(p.ProviderID, p.ProviderIncarnationID),
		p.RequestEventID,
		toolregistry.ResultStreamID(p.ToolUseID),
		stalePayload,
	)
	if err != nil {
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf(
			"claim tool call: %w",
			err,
		))
	}
	return &genregistry.ClaimToolCallResult{Disposition: string(disposition)}, nil
}

// resumeUnpublishedToolCall retries initial publication only while the
// admission that owns the retained call remains the active routing generation.
func (s *Service) resumeUnpublishedToolCall(
	ctx context.Context,
	prepared preparedToolCall,
	admission callAdmission,
) (*genregistry.CallToolResult, error) {
	registration, err := s.activeRegistration(ctx, prepared.toolset)
	if err != nil {
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf(
			"resume admitted tool call: %w",
			err,
		))
	}
	if registration.RegistrationToken != admission.registrationToken {
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf(
			"retained unpublished call belongs to inactive registration %q",
			admission.registrationToken,
		))
	}
	if err := s.validatePreparedToolCall(ctx, prepared, registration); err != nil {
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf(
			"resume admitted tool call: %w",
			err,
		))
	}
	prepared.registrationToken = admission.registrationToken
	return s.routeInitialToolCall(ctx, prepared, admission)
}

// routeInitialToolCall publishes an admitted request only when its exact
// admission has not already completed initial publication.
func (s *Service) routeInitialToolCall(
	ctx context.Context,
	prepared preparedToolCall,
	admission callAdmission,
) (*genregistry.CallToolResult, error) {
	if err := s.ensureResultStream(ctx, prepared.resultStreamID, admission.expiresAt); err != nil {
		return nil, err
	}
	if err := s.publishPreparedToolCall(ctx, prepared, admission, ""); err != nil {
		return nil, genregistry.MakeServiceUnavailable(err)
	}
	return callToolResult(
		prepared.toolUseID,
		admission,
	), nil
}

// retryPreparedToolCall republishes only when retained exact history ends in
// valid overload control for this admission.
func (s *Service) retryPreparedToolCall(
	ctx context.Context,
	prepared preparedToolCall,
	admission callAdmission,
) (*genregistry.CallToolResult, error) {
	if err := s.ensureResultStream(ctx, prepared.resultStreamID, admission.expiresAt); err != nil {
		return nil, err
	}
	if admission.overloadEventID == "" {
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf(
			"retry admitted tool call: overload control is not retained",
		))
	}
	if err := s.publishPreparedToolCall(
		ctx,
		prepared,
		admission,
		admission.overloadEventID,
	); err != nil {
		return nil, genregistry.MakeServiceUnavailable(err)
	}
	return callToolResult(
		prepared.toolUseID,
		admission,
	), nil
}

// ensureResultStream applies the call record's bounded retention and absolute
// expiration before any request can publish provider output.
func (s *Service) ensureResultStream(ctx context.Context, resultStreamID string, expiresAt time.Time) error {
	stream, err := s.pulseClient.Stream(
		resultStreamID,
		streamopts.WithStreamMaxLen(toolregistry.ResultStreamMaxLen),
		streamopts.WithStreamDeadline(expiresAt),
	)
	if err != nil {
		return genregistry.MakeServiceUnavailable(fmt.Errorf(
			"create result stream handle %q: %w",
			resultStreamID,
			err,
		))
	}
	if err := stream.Open(ctx); err != nil {
		return genregistry.MakeServiceUnavailable(fmt.Errorf(
			"open result stream %q: %w",
			resultStreamID,
			err,
		))
	}
	return nil
}

// replayCallToolResult re-establishes the registry-owned result stream before
// returning an existing admission. Terminal calls also restore their canonical
// result when retained stream history was lost.
func (s *Service) replayCallToolResult(
	ctx context.Context,
	toolUseID, resultStreamID string,
	admission callAdmission,
) (*genregistry.CallToolResult, error) {
	if err := s.ensureResultStream(ctx, resultStreamID, admission.expiresAt); err != nil {
		return nil, err
	}
	if admission.terminal {
		if err := s.callAdmissions.RestoreTerminal(ctx, admission, resultStreamID); err != nil {
			return nil, genregistry.MakeServiceUnavailable(err)
		}
	}
	return callToolResult(toolUseID, admission), nil
}

// publishLiveProviderEvent delegates provider-authenticated nonterminal
// publication to the call-admission store's Redis linearization point.
func (s *Service) publishLiveProviderEvent(
	ctx context.Context,
	toolset, providerID, providerIncarnationID, providerRegistrationToken,
	callRegistrationToken, toolUseID, requestEventID, eventName string,
	payload []byte,
) error {
	if err := s.callAdmissions.PublishLiveEvent(
		ctx,
		toolset,
		toolUseID,
		callRegistrationToken,
		providerRegistrationToken,
		providerLeaseKey(providerID, providerIncarnationID),
		requestEventID,
		toolregistry.ResultStreamID(toolUseID),
		eventName,
		payload,
	); err != nil {
		return genregistry.MakeServiceUnavailable(fmt.Errorf("publish live provider event: %w", err))
	}
	return nil
}

// publishPreparedToolCall performs one exact initial or overload publication.
// The stream append and admission commit share one Redis linearization point.
func (s *Service) publishPreparedToolCall(
	ctx context.Context,
	prepared preparedToolCall,
	admission callAdmission,
	overloadEventID string,
) error {
	return s.publishAdmittedCall(
		ctx,
		prepared.toolset,
		prepared.resultStreamID,
		prepared.message(admission),
		admission,
		overloadEventID,
	)
}

// publishAdmittedCall performs one exact initial or overload publication.
func (s *Service) publishAdmittedCall(
	ctx context.Context,
	toolset string,
	resultStreamID string,
	message toolregistry.ToolCallMessage,
	admission callAdmission,
	overloadEventID string,
) error {
	if overloadEventID != "" {
		timer := time.NewTimer(admission.overloadRetryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err := s.callAdmissions.InitializeResultStream(ctx, admission, resultStreamID); err != nil {
		return fmt.Errorf("initialize result stream %q: %w", resultStreamID, err)
	}
	if err := s.streamManager.PublishAdmittedToolCall(
		ctx,
		toolset,
		message,
		admission,
		overloadEventID,
	); err != nil {
		return fmt.Errorf("publish tool call: %w", err)
	}
	return nil
}

// activeRegistration loads the exact catalog generation used for validation
// and maps catalog failures onto the public registry contract.
func (s *Service) activeRegistration(ctx context.Context, toolset string) (catalogEntry, error) {
	registration, err := s.catalog.ActiveRegistration(ctx, toolset)
	if err == nil {
		return registration, nil
	}
	if errors.Is(err, errToolsetNotFound) {
		return catalogEntry{}, genregistry.MakeNotFound(fmt.Errorf("toolset %q not found", toolset))
	}
	return catalogEntry{}, genregistry.MakeServiceUnavailable(fmt.Errorf("get toolset: %w", err))
}

// callRejectionFromError reduces one typed pre-publication failure to the
// rejected decision replayed for every exact retry.
func callRejectionFromError(err error) (callRejection, error) {
	var serviceErr *goa.ServiceError
	if !errors.As(err, &serviceErr) {
		return callRejection{}, fmt.Errorf("reject tool call with untyped error: %w", err)
	}
	switch serviceErr.Name {
	case "not_found":
		return callRejection{kind: callRejectionNotFound, message: serviceErr.Message}, nil
	case "validation_error":
		return callRejection{kind: callRejectionValidation, message: serviceErr.Message}, nil
	case "service_unavailable":
		return callRejection{kind: callRejectionUnavailable, message: serviceErr.Message}, nil
	default:
		return callRejection{}, fmt.Errorf("reject tool call with unsupported error %q", serviceErr.Name)
	}
}

// callRejectionError restores the generated service error represented by one
// validated durable rejection.
func callRejectionError(rejection callRejection) error {
	err := errors.New(rejection.message)
	switch rejection.kind {
	case callRejectionNotFound:
		return genregistry.MakeNotFound(err)
	case callRejectionValidation:
		return genregistry.MakeValidationError(err)
	case callRejectionUnavailable:
		return genregistry.MakeCallNotAdmitted(err)
	default:
		panic(fmt.Sprintf("registry: invalid call rejection kind %q", rejection.kind))
	}
}

// callDecisionError maps the private decision-store contract to the generated
// registry boundary.
func callDecisionError(err error) error {
	var rejected *callRejectedError
	if errors.As(err, &rejected) {
		return callRejectionError(rejected.rejection)
	}
	if errors.Is(err, errCallAdmissionConflict) {
		return genregistry.MakeValidationError(err)
	}
	return genregistry.MakeServiceUnavailable(err)
}

// rejectPreparedToolCall atomically chooses the negative decision or observes
// an admission that a concurrent exact caller already committed.
func (s *Service) rejectPreparedToolCall(
	ctx context.Context,
	prepared preparedToolCall,
	cause error,
) (*genregistry.CallToolResult, error) {
	rejection, err := callRejectionFromError(cause)
	if err != nil {
		return nil, genregistry.MakeServiceUnavailable(err)
	}
	admission, err := s.callAdmissions.Reject(
		ctx,
		prepared.toolset,
		prepared.toolUseID,
		prepared.admissionDigest,
		rejection,
		s.resultStreamTTL,
	)
	if err != nil {
		return nil, callDecisionError(err)
	}
	if admission.terminal || admission.published {
		return s.replayCallToolResult(ctx, prepared.toolUseID, prepared.resultStreamID, admission)
	}
	return s.resumeUnpublishedToolCall(ctx, prepared, admission)
}

// prepareToolCallIdentity derives the token-independent immutable request
// identity used to attach global transport retries before current routing.
func prepareToolCallIdentity(
	toolset, tool string,
	payload []byte,
	meta *genregistry.ToolCallMeta,
) (preparedToolCall, error) {
	toolUseID := toolUseIDForCall(meta)
	messageMeta := toolregistry.ToolCallMeta{
		RunID:            meta.RunID,
		SessionID:        meta.SessionID,
		TurnID:           derefString(meta.TurnID),
		ToolCallID:       meta.ToolCallID,
		ParentToolCallID: derefString(meta.ParentToolCallID),
	}
	body, err := json.Marshal(struct {
		Toolset string                     `json:"toolset"`
		Tool    tools.Ident                `json:"tool"`
		Payload json.RawMessage            `json:"payload"`
		Meta    *toolregistry.ToolCallMeta `json:"meta"`
	}{
		Toolset: toolset,
		Tool:    tools.Ident(tool),
		Payload: json.RawMessage(payload),
		Meta:    &messageMeta,
	})
	if err != nil {
		return preparedToolCall{}, genregistry.MakeValidationError(fmt.Errorf(
			"encode call identity: %w",
			err,
		))
	}
	digest := sha256.Sum256(body)
	return preparedToolCall{
		toolset:         toolset,
		toolUseID:       toolUseID,
		resultStreamID:  toolregistry.ResultStreamID(toolUseID),
		tool:            tools.Ident(tool),
		payload:         json.RawMessage(payload),
		meta:            &messageMeta,
		admissionDigest: fmt.Sprintf("%x", digest[:]),
	}, nil
}

// validatePreparedToolCall validates a new or unpublished request against the
// exact active admission selected for publication.
func (s *Service) validatePreparedToolCall(
	ctx context.Context,
	prepared preparedToolCall,
	registration catalogEntry,
) error {
	var schema *genregistry.ToolSchema
	for _, candidate := range registration.Toolset.Tools {
		if candidate.Name == prepared.tool.String() {
			schema = candidate
			break
		}
	}
	if schema == nil {
		return genregistry.MakeNotFound(fmt.Errorf(
			"tool %q not found in toolset %q",
			prepared.tool,
			prepared.toolset,
		))
	}
	if err := s.validator.ValidatePayload(schema.PayloadSchema, prepared.payload); err != nil {
		return genregistry.MakeValidationError(fmt.Errorf(
			"payload validation failed: %w",
			err,
		))
	}
	health, err := s.healthTracker.Health(ctx, prepared.toolset, registration.RegistrationToken)
	if err != nil {
		return genregistry.MakeServiceUnavailable(fmt.Errorf(
			"check toolset %q health: %w",
			prepared.toolset,
			err,
		))
	}
	if !health.Healthy {
		lastPong := "missing"
		if !health.LastPong.IsZero() {
			lastPong = health.LastPong.UTC().Format(time.RFC3339Nano)
		}
		return genregistry.MakeServiceUnavailable(fmt.Errorf(
			"no healthy providers for toolset %q (staleness_threshold=%s, last_pong=%s, age=%s)",
			prepared.toolset,
			health.StalenessThreshold,
			lastPong,
			health.Age,
		))
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
func callToolResult(toolUseID string, admission callAdmission) *genregistry.CallToolResult {
	return &genregistry.CallToolResult{
		ToolUseID:             toolUseID,
		RegistrationToken:     admission.registrationToken,
		ExecutionDeadline:     admission.executionDeadline.UTC().Format(time.RFC3339Nano),
		ResultStreamExpiresAt: admission.expiresAt.UTC().Format(time.RFC3339Nano),
	}
}

// message returns the exact wire envelope for one admitted lifecycle.
func (p preparedToolCall) message(admission callAdmission) toolregistry.ToolCallMessage {
	return toolregistry.NewToolCallMessage(
		p.registrationToken,
		p.toolUseID,
		admission.executionDeadline,
		admission.expiresAt,
		p.tool,
		p.payload,
		p.meta,
	)
}

// outcomeUnknownPayload builds the canonical terminal retained before the
// provider can claim execution, so lease-loss settlement never needs model or
// provider input.
func outcomeUnknownPayload(registrationToken, toolUseID string) []byte {
	result := toolregistry.NewToolResultErrorMessage(
		registrationToken,
		toolUseID,
		toolregistry.ToolErrorCodeOutcomeUnknown,
		"tool execution outcome is unknown because the claimed provider lease was lost; the effect may have occurred",
	)
	payload, err := json.Marshal(result)
	if err != nil {
		panic(fmt.Sprintf("encode canonical outcome_unknown result: %v", err))
	}
	return payload
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
