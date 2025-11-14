package planner

import (
	"encoding/json"
	"errors"
	"testing"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestAgentMessage_RoundTrip_Strict(t *testing.T) {
	in := &AgentMessage{
		Role: "assistant",
		Parts: []model.Part{
			model.TextPart{Text: "hello"},
			model.ThinkingPart{Text: "reasoning..."},
			model.ToolUsePart{
				ID:    "tu_1",
				Name:  "search",
				Input: map[string]any{"q": "golang"},
			},
			model.ToolResultPart{
				ToolUseID: "tu_1",
				Content:   map[string]any{"hits": 3},
				IsError:   false,
			},
		},
		Meta: map[string]any{"k": "v"},
	}

	b, err := json.Marshal(in)
	if err != nil {
		t.Errorf("marshal: %v", err)
		return
	}

	var out AgentMessage
	if err := json.Unmarshal(b, &out); err != nil {
		t.Errorf("unmarshal: %v", err)
		return
	}

	if out.Role != in.Role {
		t.Errorf("role mismatch: %q != %q", out.Role, in.Role)
		return
	}
	if len(out.Parts) != len(in.Parts) {
		t.Errorf("parts length mismatch: %d != %d", len(out.Parts), len(in.Parts))
		return
	}

	// Validate types and essential fields
	if tp, ok := out.Parts[0].(model.TextPart); !ok || tp.Text != "hello" {
		t.Errorf("parts[0] want TextPart 'hello', got %#v", out.Parts[0])
		return
	}
	if th, ok := out.Parts[1].(model.ThinkingPart); !ok || th.Text != "reasoning..." {
		t.Errorf("parts[1] want ThinkingPart 'reasoning...', got %#v", out.Parts[1])
		return
	}
	if tu, ok := out.Parts[2].(model.ToolUsePart); !ok || tu.Name != "search" || tu.ID != "tu_1" {
		t.Errorf("parts[2] want ToolUsePart search/tu_1, got %#v", out.Parts[2])
		return
	}
	if tr, ok := out.Parts[3].(model.ToolResultPart); !ok || tr.ToolUseID != "tu_1" || tr.IsError {
		t.Errorf("parts[3] want ToolResultPart tu_1 !error, got %#v", out.Parts[3])
		return
	}
	if out.Meta["k"] != "v" {
		t.Errorf("meta mismatch: %#v", out.Meta)
		return
	}
}

func TestAgentMessage_Unmarshal_InvalidCasing_Toplevel(t *testing.T) {
	// lowercased keys should be rejected by strict unmarshal
	js := []byte(`{"role":"assistant","parts":[{"Text":"hello"}],"meta":{"k":"v"}}`)
	var m AgentMessage
	err := json.Unmarshal(js, &m)
	if err == nil {
		t.Errorf("expected error for invalid top-level casing, got nil")
		return
	}
	if !errors.Is(err, err) { // basic non-nil assertion already above; keep branch for readability
		t.Errorf("unexpected error: %v", err)
		return
	}
}

func TestAgentMessage_Unmarshal_InvalidCasing_Parts(t *testing.T) {
	// toolUseId should be rejected; expect an error
	js := []byte(`{"Role":"assistant","Parts":[{"toolUseId":"x","Content":{}}]}`)
	var m AgentMessage
	err := json.Unmarshal(js, &m)
	if err == nil {
		t.Errorf("expected error for invalid part casing, got nil")
		return
	}
}
