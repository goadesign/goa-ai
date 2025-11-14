package planner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/model"
)

// UnmarshalJSON implements custom decoding for AgentMessage so that the Parts
// slice, which is typed as []model.Part (an interface), can be materialized
// from generic JSON payloads such as those produced by Temporal's JSON codec.
func (m *AgentMessage) UnmarshalJSON(data []byte) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}

	// Reject lowercase keys to enforce strict casing
	if _, ok := obj["role"]; ok {
		return errors.New("invalid key casing: use 'Role' not 'role'")
	}
	if _, ok := obj["meta"]; ok {
		return errors.New("invalid key casing: use 'Meta' not 'meta'")
	}
	if _, ok := obj["parts"]; ok {
		return errors.New("invalid key casing: use 'Parts' not 'parts'")
	}

	if v, ok := obj["Role"]; ok {
		var r string
		if err := json.Unmarshal(v, &r); err != nil {
			return fmt.Errorf("decode Role: %w", err)
		}
		m.Role = r
	}
	if v, ok := obj["Meta"]; ok {
		var meta map[string]any
		if err := json.Unmarshal(v, &meta); err != nil {
			return fmt.Errorf("decode Meta: %w", err)
		}
		m.Meta = meta
	}
	if v, ok := obj["Parts"]; ok {
		var raws []json.RawMessage
		if err := json.Unmarshal(v, &raws); err != nil {
			return fmt.Errorf("decode Parts: %w", err)
		}
		if len(raws) == 0 {
			m.Parts = nil
			return nil
		}
		parts := make([]model.Part, 0, len(raws))
		for i := range raws {
			p, err := decodeModelPart(raws[i])
			if err != nil {
				return fmt.Errorf("decode parts[%d]: %w", i, err)
			}
			parts = append(parts, p)
		}
		m.Parts = parts
	} else {
		m.Parts = nil
	}
	return nil
}

// decodeModelPart inspects the JSON shape and decodes into the appropriate
// concrete model.Part implementation. It supports:
//   - model.TextPart       {"Text": "..."} or a bare JSON string
//   - model.ThinkingPart   {"Kind": "thinking", "Text": "..."}
//     (Note: plain {"Text": "..."} is treated as TextPart)
//   - model.ToolUsePart    {"Name": "...", "Input": ...} (optional "ID")
//   - model.ToolResultPart {"ToolUseID": "...", "Content": ..., "IsError": bool}
func decodeModelPart(b json.RawMessage) (model.Part, error) {
	raw := bytes.TrimSpace([]byte(b))
	if len(raw) == 0 {
		return nil, errors.New("empty part")
	}

	// Strong contract: parts must be objects with exact expected keys.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("decode object part: %w", err)
	}

	// ThinkingPart: explicit kind discriminator
	if kv, ok := obj["Kind"]; ok {
		var kind string
		if err := json.Unmarshal(kv, &kind); err != nil {
			return nil, fmt.Errorf("decode Kind: %w", err)
		}
		if kind == "thinking" {
			var s string
			if v, ok2 := obj["Text"]; ok2 {
				if err := json.Unmarshal(v, &s); err != nil {
					return nil, fmt.Errorf("decode ThinkingPart.text: %w", err)
				}
			}
			return model.ThinkingPart{
				Text: s,
			}, nil
		}
	}

	// TextPart
	if v, ok := obj["Text"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return nil, fmt.Errorf("decode TextPart.text: %w", err)
		}
		return model.TextPart{
			Text: s,
		}, nil
	}

	// ToolResultPart (require ToolUseID at minimum)
	if _, ok := obj["ToolUseID"]; ok {
		var pr model.ToolResultPart
		if err := json.Unmarshal(raw, &pr); err != nil {
			return nil, fmt.Errorf("decode ToolResultPart: %w", err)
		}
		if pr.ToolUseID == "" {
			return nil, errors.New("ToolResultPart requires ToolUseID")
		}
		return pr, nil
	}

	// ToolUsePart (require Name and Input)
	if _, hasName := obj["Name"]; hasName {
		if _, hasInput := obj["Input"]; hasInput {
			var pu model.ToolUsePart
			if err := json.Unmarshal(raw, &pu); err != nil {
				return nil, fmt.Errorf("decode ToolUsePart: %w", err)
			}
			if pu.Name == "" {
				return nil, errors.New("ToolUsePart requires Name")
			}
			return pu, nil
		}
	}

	return nil, errors.New("unknown part shape (expected Text, ThinkingPart, ToolUsePart, or ToolResultPart)")
}
