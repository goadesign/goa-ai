// Package registry binds tool calls to the registration that supplied their definition.
// The registry checks that registration before admission and during atomic
// publication. Stored call identity includes the selection, so retries cannot
// switch to another definition or to the unbound CallTool operation.
package registry

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
)

// ResolveToolset returns the definition and token from one active catalog read.
// Provider replacement after this read is detected when CallResolvedTool runs.
func (s *Service) ResolveToolset(ctx context.Context, p *genregistry.GetToolsetPayload) (*genregistry.ResolvedToolset, error) {
	registration, err := s.activeRegistration(ctx, p.Name)
	if err != nil {
		return nil, err
	}
	return &genregistry.ResolvedToolset{
		Toolset:           registration.Toolset,
		RegistrationToken: registration.RegistrationToken,
	}, nil
}

// CallResolvedTool admits or resumes a call against its resolved registration.
// A published call keeps its original result even after that registration ends.
func (s *Service) CallResolvedTool(ctx context.Context, p *genregistry.CallResolvedToolPayload) (*genregistry.CallToolResult, error) {
	if err := toolregistry.ValidateWireProtocolVersion(p.WireProtocolVersion); err != nil {
		return nil, genregistry.MakeValidationError(err)
	}
	prepared, err := prepareToolCallIdentity(p.Toolset, p.Tool, p.PayloadJSON, p.Meta)
	if err != nil {
		return nil, err
	}
	prepared.expectedRegistrationToken = p.ExpectedRegistrationToken
	digest := sha256.Sum256([]byte(
		"resolved-tool-call-v1\x00" + prepared.admissionDigest + "\x00" + p.ExpectedRegistrationToken,
	))
	prepared.admissionDigest = fmt.Sprintf("%x", digest[:])
	admission, err := s.callAdmissions.Attach(
		ctx,
		prepared.toolset,
		prepared.toolUseID,
		prepared.admissionDigest,
	)
	if err == nil {
		switch {
		case admission.terminal:
			return s.replayCallToolResult(ctx, prepared.toolUseID, prepared.resultStreamID, admission)
		case admission.overloadEventID != "":
			return s.retryExactToolCall(ctx, prepared, admission)
		case admission.published:
			return s.replayCallToolResult(ctx, prepared.toolUseID, prepared.resultStreamID, admission)
		default:
			return s.routeUnpublishedToolCall(ctx, prepared, admission.executionDeadline)
		}
	}
	if !errors.Is(err, errCallAdmissionNotFound) {
		return nil, callDecisionError(err)
	}
	return s.routeUnpublishedToolCall(ctx, prepared, time.Now().Add(s.executionTimeout))
}
