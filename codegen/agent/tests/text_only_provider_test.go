// These tests call a regenerated Registry provider and inspect the exact input
// received by its bound service, including metadata and evidence output.
package tests

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genprovider "goa.design/goa-ai/internal/testpresentation/gen/record_provider"
	genqueries "goa.design/goa-ai/internal/testpresentation/gen/record_provider/toolsets/provider_queries"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/goa-ai/runtime/toolregistry/contract"
)

type textOnlyProviderService struct {
	received  *genprovider.ReadPayload
	textOnly  bool
	toolUseID string
	calls     int
}

func TestGeneratedTextOnlyProviderPreservesExecutionInput(t *testing.T) {
	for _, test := range []struct {
		name       string
		textOnly   bool
		input      string
		accepted   bool
		pointerSet bool
		render     bool
	}{
		{"text-only omitted controls", true, `{"query":"active","page_token":"second"}`, true, true, false},
		{"text-only false controls", true, `{"query":"active","page_token":"second","render_ui":false,"render_summary":false}`, true, true, false},
		{"text-only true card", true, `{"query":"active","page_token":"second","render_ui":true}`, false, false, false},
		{"text-only true summary", true, `{"query":"active","page_token":"second","render_summary":true}`, false, false, false},
		{"ordinary true controls", false, `{"query":"active","page_token":"second","render_ui":true,"render_summary":true}`, true, true, true},
		{"ordinary omitted controls", false, `{"query":"active","page_token":"second"}`, true, false, false},
		{"text-only supplied injected field", true, `{"query":"active","page_token":"second","session_id":"untrusted"}`, false, false, false},
		{"ordinary supplied injected field", false, `{"query":"active","page_token":"second","session_id":"untrusted"}`, false, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc := &textOnlyProviderService{}
			provider := genqueries.NewProvider(svc)
			ctx := toolregistry.WithToolUseID(t.Context(), "use-1")
			msg := toolregistry.ToolCallMessage{
				ToolUseID:         "use-1",
				RegistrationToken: "generation-1",
				Tool:              genqueries.Read,
				Payload:           []byte(test.input),
				Meta: &toolregistry.ToolCallMeta{
					TextOnly:   test.textOnly,
					SessionID:  "session-1",
					RunID:      "run-1",
					ToolCallID: "call-1",
				},
			}
			result, err := provider.HandleToolCall(ctx, msg)
			require.NoError(t, err)
			assert.Equal(t, test.input, string(msg.Payload))
			assert.Equal(t, "generation-1", result.RegistrationToken)
			assert.Equal(t, "use-1", result.ToolUseID)
			if !test.accepted {
				require.NotNil(t, result.Error)
				assert.Equal(t, "invalid_arguments", result.Error.Code)
				assert.Zero(t, svc.calls)
				return
			}
			require.Nil(t, result.Error)
			require.NotNil(t, svc.received)
			assert.Equal(t, 1, svc.calls)
			assert.Equal(t, "active", svc.received.Query)
			assert.Equal(t, "session-1", svc.received.SessionID)
			require.NotNil(t, svc.received.PageToken)
			assert.Equal(t, "second", *svc.received.PageToken)
			assert.Equal(t, test.textOnly, svc.textOnly)
			assert.Equal(t, "use-1", svc.toolUseID)
			if test.pointerSet {
				require.NotNil(t, svc.received.RenderUI)
				assert.Equal(t, test.render, *svc.received.RenderUI)
			} else {
				assert.Nil(t, svc.received.RenderUI)
			}
			assert.Equal(t, test.render, svc.received.RenderSummary)
			assert.JSONEq(t, `{"count":2}`, string(result.Result))
			require.Len(t, result.ServerData, 1)
			assert.Equal(t, "evidence", result.ServerData[0].Audience)
			assert.JSONEq(t, `"sample"`, string(result.ServerData[0].Data))
		})
	}
}

func TestGeneratedTextOnlyExecutionMatchesOrdinaryInjectedFields(t *testing.T) {
	ordinary := genqueries.SpecRead()
	for _, spec := range []struct {
		name     string
		input    string
		accepted bool
	}{
		{"execution page", `{"query":"active","page_token":"second"}`, true},
		{"injected session", `{"query":"active","session_id":"untrusted"}`, false},
	} {
		t.Run(spec.name, func(t *testing.T) {
			for _, codec := range []func([]byte) (any, error){ordinary.ExecutionPayloadCodec.FromJSON, ordinary.ForTextOnly().ExecutionPayloadCodec.FromJSON} {
				_, err := codec([]byte(spec.input))
				assert.Equal(t, spec.accepted, err == nil)
			}
			for _, declaration := range genqueries.ToolSchemas() {
				remote, err := contract.Compile(declaration)
				require.NoError(t, err)
				for _, codec := range []func([]byte) (any, error){remote.ExecutionPayloadCodec.FromJSON, remote.ForTextOnly().ExecutionPayloadCodec.FromJSON} {
					_, err = codec([]byte(spec.input))
					assert.Equal(t, spec.accepted, err == nil)
				}
			}
		})
	}
}

// Read saves the decoded method input and returns a count with evidence so the
// test observes input preservation and the provider's permitted output.
func (s *textOnlyProviderService) Read(ctx context.Context, payload *genprovider.ReadPayload) (*genprovider.ReadResult, error) {
	s.received = payload
	s.textOnly = run.IsTextOnly(ctx)
	s.toolUseID, _ = toolregistry.ToolUseIDFromContext(ctx)
	s.calls++
	return &genprovider.ReadResult{Count: 2, Reference: "sample"}, nil
}
