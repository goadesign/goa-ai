package a2a

import (
	"context"
	"fmt"

	"goa.design/goa-ai/runtime/agent/planner"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// ProviderConfig contains static configuration for consuming a remote A2A
	// provider. It is generated from the provider design and used by consumers
	// to register toolsets without duplicating mapping logic.
	ProviderConfig struct {
		// Suite is the canonical A2A suite identifier (for example,
		// "service.agent.toolset").
		Suite string
		// Skills enumerates the skills exposed by the provider. Each entry maps
		// directly to a tool in the planner/runtime, using SkillConfig.ID as the
		// canonical tool identifier.
		Skills []SkillConfig
	}
)

// NewProviderToolsetRegistration constructs a ToolsetRegistration for a remote
// A2A provider. Callers typically use RegisterProvider instead, but tests and
// advanced integrations may use this helper directly.
func NewProviderToolsetRegistration(caller Caller, cfg ProviderConfig) agentruntime.ToolsetRegistration {
	skillMap := make(map[tools.Ident]SkillConfig, len(cfg.Skills))
	for _, sk := range cfg.Skills {
		id := tools.Ident(sk.ID)
		skillMap[id] = sk
	}

	exec := func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
		if call == nil {
			return nil, fmt.Errorf("tool request is required")
		}

		skill, ok := skillMap[call.Name]
		if !ok {
			result := planner.ToolResult{
				Name:  call.Name,
				Error: planner.ToolErrorf("unknown skill: %s", call.Name),
			}
			return &result, nil
		}

		decoded, err := skill.Payload.Codec.FromJSON(call.Payload)
		if err != nil {
			result := planner.ToolResult{
				Name:  call.Name,
				Error: planner.ToolErrorFromError(err),
			}
			return &result, nil
		}

		payload, err := skill.Payload.Codec.ToJSON(decoded)
		if err != nil {
			result := planner.ToolResult{
				Name:  call.Name,
				Error: planner.ToolErrorFromError(err),
			}
			return &result, nil
		}

		resp, err := caller.SendTask(ctx, SendTaskRequest{
			Suite:   cfg.Suite,
			Skill:   skill.ID,
			Payload: payload,
		})
		if err != nil {
			result := handleError(call.Name, skillMap, err)
			return &result, nil
		}

		var value any
		if len(resp.Result) > 0 {
			value, err = skill.Result.Codec.FromJSON(resp.Result)
			if err != nil {
				result := planner.ToolResult{
					Name:  call.Name,
					Error: planner.ToolErrorFromError(err),
				}
				return &result, nil
			}
		}

		return &planner.ToolResult{
			Name:      call.Name,
			Result:    value,
			Telemetry: ExtractTelemetry(resp),
		}, nil
	}

	return agentruntime.ToolsetRegistration{
		Name:             cfg.Suite,
		Description:      "",
		Execute:          exec,
		Specs:            buildToolSpecs(cfg),
		DecodeInExecutor: true,
	}
}

// RegisterProvider registers a remote A2A provider with the agent runtime
// using the given Caller and ProviderConfig. It builds a ToolsetRegistration
// and delegates to Runtime.RegisterToolset.
func RegisterProvider(_ context.Context, rt *agentruntime.Runtime, caller Caller, cfg ProviderConfig) error {
	if rt == nil {
		return fmt.Errorf("runtime is required")
	}
	if caller == nil {
		return fmt.Errorf("caller is required")
	}
	if cfg.Suite == "" {
		return fmt.Errorf("suite is required")
	}
	if len(cfg.Skills) == 0 {
		return fmt.Errorf("at least one skill is required")
	}
	return rt.RegisterToolset(NewProviderToolsetRegistration(caller, cfg))
}

func buildToolSpecs(cfg ProviderConfig) []tools.ToolSpec {
	specs := make([]tools.ToolSpec, 0, len(cfg.Skills))
	for _, sk := range cfg.Skills {
		specs = append(specs, tools.ToolSpec{
			Name:        tools.Ident(sk.ID),
			Service:     "",
			Toolset:     cfg.Suite,
			Description: sk.Description,
			Payload:     sk.Payload,
			Result:      sk.Result,
		})
	}
	return specs
}

func handleError(toolName tools.Ident, skills map[tools.Ident]SkillConfig, err error) planner.ToolResult {
	result := planner.ToolResult{
		Name:  toolName,
		Error: planner.ToolErrorFromError(err),
	}
	if hint := DefaultRetryHint(skills, toolName, err); hint != nil {
		result.RetryHint = hint
	}
	return result
}
