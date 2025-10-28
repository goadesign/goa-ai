package assistantapi

import (
	context "context"
	"fmt"

	orchestrator "example.com/assistant/gen/orchestrator"
	"github.com/google/uuid"
	apitypes "goa.design/goa-ai/agents/apitypes"
)

type orchestratorsrvc struct {
	runtime *RuntimeHarness
}

// NewOrchestrator returns the orchestrator service implementation.
func NewOrchestrator() orchestrator.Service {
	h, err := NewRuntimeHarness(context.Background())
	if err != nil {
		panic(fmt.Sprintf("runtime harness: %v", err))
	}
	return &orchestratorsrvc{runtime: h}
}

func (s *orchestratorsrvc) Run(ctx context.Context, payload *orchestrator.AgentRunPayload) (*orchestrator.AgentRunResult, error) {
	return s.runAgent(ctx, payload)
}

func (s *orchestratorsrvc) RunEndpoint(ctx context.Context, payload *orchestrator.AgentRunPayload) (*orchestrator.AgentRunResult, error) {
	return s.runAgent(ctx, payload)
}

func (s *orchestratorsrvc) runAgent(ctx context.Context, payload *orchestrator.AgentRunPayload) (*orchestrator.AgentRunResult, error) {
	if payload == nil {
		return nil, fmt.Errorf("payload required")
	}
	apiInput := payload.ConvertToRunInput()
	if apiInput.AgentID == "" {
		apiInput.AgentID = "orchestrator.chat"
	}
	if apiInput.RunID == "" {
		apiInput.RunID = uuid.NewString()
	}
	runtimeInput, err := apitypes.ToRuntimeRunInput(apiInput)
	if err != nil {
		return nil, fmt.Errorf("convert run input: %w", err)
	}
	out, err := s.runtime.Run(ctx, runtimeInput)
	if err != nil {
		return nil, err
	}
	apiOutput := apitypes.FromRuntimeRunOutput(out)
	if apiOutput.AgentID == "" {
		apiOutput.AgentID = runtimeInput.AgentID
	}
	if apiOutput.RunID == "" {
		apiOutput.RunID = runtimeInput.RunID
	}
	result := new(orchestrator.AgentRunResult)
	result.CreateFromRunOutput(apiOutput)
	return result, nil
}
