package planner

import (
	"encoding/json"
	"fmt"

	"goa.design/goa-ai/runtime/agent/model"
)

// MarshalJSON ensures AgentMessage.Parts round-trip with type fidelity,
// disambiguating ThinkingPart from TextPart via a simple kind discriminator.
func (m AgentMessage) MarshalJSON() ([]byte, error) {
	//nolint:tagliatelle // Intentional casing to disambiguate kinds in custom JSON
	type alias struct {
		Role  string         `json:"Role,omitempty"`
		Parts []any          `json:"Parts,omitempty"`
		Meta  map[string]any `json:"Meta,omitempty"`
	}
	out := alias{
		Role: m.Role,
		Meta: m.Meta,
	}
	if len(m.Parts) > 0 {
		out.Parts = make([]any, 0, len(m.Parts))
		for _, p := range m.Parts {
			switch v := p.(type) {
			case model.TextPart:
				out.Parts = append(out.Parts, v)
			case model.ThinkingPart:
				// Encode as {"Kind":"thinking","Text":"..."}
				//nolint:tagliatelle // Intentional casing for compatibility with existing logs
				out.Parts = append(out.Parts, struct {
					Kind string `json:"Kind"`
					Text string `json:"Text"`
				}{
					Kind: "thinking",
					Text: v.Text,
				})
			case model.ToolUsePart:
				out.Parts = append(out.Parts, v)
			case model.ToolResultPart:
				out.Parts = append(out.Parts, v)
			default:
				return nil, fmt.Errorf("unsupported part type %T", p)
			}
		}
	}
	return json.Marshal(out)
}
