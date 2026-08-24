// Package planner tests typed tool-call construction at the application
// boundary where identifiers and payload encoding can fail.
package planner

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/tools"
)

func TestNewToolRequestLeavesExecutionIdentityToRuntime(t *testing.T) {
	tool := tools.TypedTool[string, any]{
		Name: "svc.lookup",
		Payload: tools.JSONCodec[string]{
			ToJSON: func(value string) ([]byte, error) {
				return []byte(value), nil
			},
		},
	}

	request, err := NewToolRequest(tool, "value")

	require.NoError(t, err)
	require.Equal(t, tools.Ident("svc.lookup"), request.Name)
	require.Empty(t, request.ModelToolCallID)
}

func TestNewToolRequestReturnsPayloadEncodingError(t *testing.T) {
	encodeErr := errors.New("invalid payload")
	tool := tools.TypedTool[string, any]{
		Name: "svc.lookup",
		Payload: tools.JSONCodec[string]{
			ToJSON: func(string) ([]byte, error) {
				return nil, encodeErr
			},
		},
	}

	request, err := NewToolRequest(tool, "value")

	require.Empty(t, request)
	require.ErrorIs(t, err, encodeErr)
	require.EqualError(t, err, "encode svc.lookup tool payload: invalid payload")
}
