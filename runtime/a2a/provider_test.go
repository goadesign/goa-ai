package a2a

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/planner"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/tools"
)

type recordingCaller struct {
	lastReq SendTaskRequest
	resp    SendTaskResponse
	err     error
}

func (c *recordingCaller) SendTask(_ context.Context, req SendTaskRequest) (SendTaskResponse, error) {
	c.lastReq = req
	return c.resp, c.err
}

type countingCodec struct {
	toCount   int
	fromCount int
}

func (c *countingCodec) ToJSON(v any) ([]byte, error) {
	c.toCount++
	return json.Marshal(v)
}

func (c *countingCodec) FromJSON(b []byte) (any, error) {
	c.fromCount++
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// TestToolRequestToSendTaskRequestProperty verifies Property 4:
// ToolRequest to SendTaskRequest mapping.
// **Feature: a2a-architecture-redesign, Property 4: ToolRequest to SendTaskRequest Mapping**
// *For any* ToolRequest with a valid tool name, the mapping to SendTaskRequest
// should use the exact skill ID from SkillConfig without any string manipulation.
// **Validates: Requirements 3.2, 5.5**
func TestToolRequestToSendTaskRequestProperty(t *testing.T) {
	t.Helper()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 50
	properties := gopter.NewProperties(parameters)

	properties.Property("SendTaskRequest uses exact suite and skill ID", prop.ForAll(
		func(suite, skillID string) bool {
			if suite == "" {
				suite = "svc.agent.tools"
			}
			if skillID == "" {
				skillID = "tools.echo"
			}
			fullID := tools.Ident(skillID)

			codec := &countingCodec{}
			payloadSpec := tools.TypeSpec{
				Name:   "Payload",
				Schema: []byte(`{"type":"object"}`),
				Codec: tools.JSONCodec[any]{
					ToJSON:   codec.ToJSON,
					FromJSON: codec.FromJSON,
				},
			}
			resultSpec := tools.TypeSpec{
				Name:   "Result",
				Schema: []byte(`{"type":"object"}`),
				Codec: tools.JSONCodec[any]{
					ToJSON:   codec.ToJSON,
					FromJSON: codec.FromJSON,
				},
			}

			cfg := ProviderConfig{
				Suite: suite,
				Skills: []SkillConfig{
					{
						ID:          skillID,
						Description: "echo",
						Payload:     payloadSpec,
						Result:      resultSpec,
						ExampleArgs: `{"msg":"hello"}`,
					},
				},
			}

			caller := &recordingCaller{
				resp: SendTaskResponse{
					Result: json.RawMessage(`{"ok":true}`),
				},
			}

			reg := NewProviderToolsetRegistration(caller, cfg)
			require.Equal(t, suite, reg.Name)
			require.Len(t, reg.Specs, 1)
			require.Equal(t, fullID, reg.Specs[0].Name)
			require.Equal(t, suite, reg.Specs[0].Toolset)

			raw := json.RawMessage(`{"msg":"hello"}`)
			call := &planner.ToolRequest{
				Name:    fullID,
				Payload: raw,
			}

			result, err := reg.Execute(context.Background(), call)
			if err != nil {
				return false
			}
			if result == nil {
				return false
			}

			// Verify mapping: suite and skill ID are used as-is.
			if caller.lastReq.Suite != suite {
				return false
			}
			if caller.lastReq.Skill != skillID {
				return false
			}

			// Verify codec usage: both ToJSON and FromJSON should be called.
			if codec.toCount == 0 || codec.fromCount == 0 {
				return false
			}

			return true
		},
		gen.AlphaString(),
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// TestErrorToRetryHintProperty verifies Property 6: RetryHint from SkillConfig.
// **Feature: a2a-architecture-redesign, Property 6: RetryHint from SkillConfig**
// *For any* A2A error requiring a retry hint, the schema and example in the hint
// should match the values from the corresponding SkillConfig.
// **Validates: Requirements 3.4, 5.6**
func TestErrorToRetryHintProperty(t *testing.T) {
	t.Helper()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 50
	properties := gopter.NewProperties(parameters)

	properties.Property("invalid params error produces retry hint", prop.ForAll(
		func(skillID, schema, example, message string) bool {
			if skillID == "" {
				skillID = "tools.echo"
			}
			if schema == "" {
				schema = `{"type":"object"}`
			}
			if example == "" {
				example = `{"msg":"hello"}`
			}
			if message == "" {
				message = "invalid params"
			}

			skill := SkillConfig{
				ID: skillID,
				Payload: tools.TypeSpec{
					Name:   "Payload",
					Schema: []byte(schema),
				},
				ExampleArgs: example,
			}
			err := &Error{Code: JSONRPCInvalidParams, Message: message}

			hint := ErrorToRetryHint(skill, err)
			if hint == nil {
				return false
			}
			if hint.Tool != tools.Ident(skillID) {
				return false
			}
			if hint.Reason != planner.RetryReasonInvalidArguments {
				return false
			}
			if !hint.RestrictToTool {
				return false
			}
			// Prompt should contain both the schema and example.
			if !contains(hint.Message, schema) || !contains(hint.Message, example) {
				return false
			}
			return true
		},
		gen.AlphaString(),
		gen.AlphaString(),
		gen.AlphaString(),
		gen.AlphaString(),
	))

	properties.Property("method not found error produces tool unavailable hint", prop.ForAll(
		func(skillID, message string) bool {
			if skillID == "" {
				skillID = "tools.echo"
			}
			if message == "" {
				message = "not found"
			}

			skill := SkillConfig{ID: skillID}
			err := &Error{Code: JSONRPCMethodNotFound, Message: message}

			hint := ErrorToRetryHint(skill, err)
			if hint == nil {
				return false
			}
			if hint.Tool != tools.Ident(skillID) {
				return false
			}
			if hint.Reason != planner.RetryReasonToolUnavailable {
				return false
			}
			if hint.Message != message {
				return false
			}
			return true
		},
		gen.AlphaString(),
		gen.AlphaString(),
	))

	properties.Property("DefaultRetryHint looks up skill by ident", prop.ForAll(
		func(skillID string) bool {
			if skillID == "" {
				skillID = "tools.echo"
			}
			id := tools.Ident(skillID)
			skill := SkillConfig{ID: skillID}
			err := &Error{Code: JSONRPCMethodNotFound, Message: "missing"}

			skillMap := map[tools.Ident]SkillConfig{id: skill}
			hint := DefaultRetryHint(skillMap, id, err)
			if hint == nil {
				return false
			}
			return hint.Tool == id
		},
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// TestRegisterProviderValidation verifies basic RegisterProvider argument
// validation behavior.
// **Feature: a2a-architecture-redesign**
// **Validates: Requirements 3.1**
func TestRegisterProviderValidation(t *testing.T) {
	t.Helper()

	rt := agentruntime.New()
	caller := &recordingCaller{}
	cfg := ProviderConfig{}

	err := RegisterProvider(context.Background(), nil, caller, cfg)
	require.Error(t, err)

	err = RegisterProvider(context.Background(), rt, nil, cfg)
	require.Error(t, err)

	cfg.Suite = "svc.agent.tools"
	cfg.Skills = []SkillConfig{
		{
			ID:          "tools.echo",
			Description: "echo",
			Payload: tools.TypeSpec{
				Name: "Payload",
			},
			Result: tools.TypeSpec{
				Name: "Result",
			},
		},
	}

	// Toolsets map is unexported; use RegisterToolset to verify success signal.
	rt = agentruntime.New()
	err = RegisterProvider(context.Background(), rt, caller, cfg)
	require.NoError(t, err)
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
