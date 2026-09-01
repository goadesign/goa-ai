// This file verifies the MCP expressions that Goa evaluates before code
// generation starts.
package mcp

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/expr"
)

func TestMCPExpr_EvalName(t *testing.T) {
	m := &MCPExpr{
		Name:    "test-server",
		Service: &expr.ServiceExpr{Name: "test-service"},
	}
	require.Equal(t, "MCP server for test-service", m.EvalName())
}

func TestMCPExpr_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mcp     *MCPExpr
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid MCP server",
			mcp: &MCPExpr{
				Name:    "test-server",
				Version: "1.0.0",
				Service: &expr.ServiceExpr{Name: "test-service"},
			},
			wantErr: false,
		},
		{
			name: "missing name",
			mcp: &MCPExpr{
				Version: "1.0.0",
				Service: &expr.ServiceExpr{Name: "test-service"},
			},
			wantErr: true,
			errMsg:  "MCP server name is required",
		},
		{
			name: "missing version",
			mcp: &MCPExpr{
				Name:    "test-server",
				Service: &expr.ServiceExpr{Name: "test-service"},
			},
			wantErr: true,
			errMsg:  "MCP server version is required",
		},
		{
			name: "unsupported protocol version",
			mcp: &MCPExpr{
				Name:            "test-server",
				Version:         "1.0.0",
				ProtocolVersion: "2099-01-01",
				Service:         &expr.ServiceExpr{Name: "test-service"},
			},
			wantErr: true,
			errMsg:  `protocol version must be "2025-06-18"`,
		},
		{
			name: "duplicate tool name",
			mcp: &MCPExpr{
				Name:    "test-server",
				Version: "1.0.0",
				Service: &expr.ServiceExpr{Name: "test-service"},
				Tools: []*ToolExpr{
					{Name: "search", Description: "Search documents"},
					{Name: "search", Description: "Search reports"},
				},
			},
			wantErr: true,
			errMsg:  `tool name "search" is used more than once`,
		},
		{
			name: "duplicate resource URI",
			mcp: &MCPExpr{
				Name:    "test-server",
				Version: "1.0.0",
				Service: &expr.ServiceExpr{Name: "test-service"},
				Resources: []*ResourceExpr{
					{Name: "guide", URI: "file:///guide", MimeType: "text/plain"},
					{Name: "manual", URI: "file:///guide", MimeType: "text/plain"},
				},
			},
			wantErr: true,
			errMsg:  `resource URI "file:///guide" is used more than once`,
		},
		{
			name: "duplicate prompt name",
			mcp: &MCPExpr{
				Name:    "test-server",
				Version: "1.0.0",
				Service: &expr.ServiceExpr{Name: "test-service"},
				Prompts: []*PromptExpr{
					{Name: "review", Messages: []*MessageExpr{{Role: "user", Content: "Review this"}}},
					{Name: "review", Messages: []*MessageExpr{{Role: "user", Content: "Check this"}}},
				},
			},
			wantErr: true,
			errMsg:  `prompt name "review" is used more than once`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previousRoot := expr.Root
			t.Cleanup(func() {
				expr.Root = previousRoot
			})
			expr.Root = &expr.RootExpr{API: &expr.APIExpr{
				JSONRPC: &expr.JSONRPCExpr{HTTPExpr: expr.HTTPExpr{Services: []*expr.HTTPServiceExpr{{
					ServiceExpr: tt.mcp.Service,
					JSONRPCRoute: &expr.RouteExpr{
						Method: http.MethodPost,
						Path:   "/mcp",
					},
				}}}},
			}}
			err := tt.mcp.Validate()
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMCPExpr_Finalize(t *testing.T) {
	t.Run("sets default protocol version", func(t *testing.T) {
		m := &MCPExpr{}
		m.Finalize()
		require.Equal(t, "2025-06-18", m.ProtocolVersion)
	})

	t.Run("preserves authored protocol version", func(t *testing.T) {
		m := &MCPExpr{ProtocolVersion: "2099-01-01"}
		m.Finalize()
		require.Equal(t, "2099-01-01", m.ProtocolVersion)
	})
}

func TestToolExpr_Validate(t *testing.T) {
	tests := []struct {
		name    string
		tool    *ToolExpr
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid tool",
			tool: &ToolExpr{
				Name:        "test-tool",
				Description: "A test tool",
			},
			wantErr: false,
		},
		{
			name: "missing name",
			tool: &ToolExpr{
				Description: "A test tool",
			},
			wantErr: true,
			errMsg:  "tool name is required",
		},
		{
			name: "missing description",
			tool: &ToolExpr{
				Name: "test-tool",
			},
			wantErr: true,
			errMsg:  "tool description is required",
		},
		{
			name: "streaming method",
			tool: &ToolExpr{
				Name:        "test-tool",
				Description: "A test tool",
				Method: &expr.MethodExpr{
					Name:   "watch",
					Stream: expr.ServerStreamKind,
				},
			},
			wantErr: true,
			errMsg:  `tool "test-tool" uses streaming method "watch"; MCP tools must return one result from one request`,
		},
		{
			name: "primitive payload",
			tool: &ToolExpr{
				Name:        "test-tool",
				Description: "A test tool",
				Method: &expr.MethodExpr{
					Name:    "call",
					Payload: &expr.AttributeExpr{Type: expr.String},
				},
			},
			wantErr: true,
			errMsg:  `tool "test-tool" method "call" payload must be an object`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.tool.Validate()
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestResourceExpr_Validate(t *testing.T) {
	tests := []struct {
		name     string
		resource *ResourceExpr
		wantErr  bool
		errMsg   string
	}{
		{
			name: "valid resource",
			resource: &ResourceExpr{
				Name:     "test-resource",
				URI:      "file:///test",
				MimeType: "text/plain",
				Method: &expr.MethodExpr{
					Name:   "read",
					Result: &expr.AttributeExpr{Type: expr.String},
				},
			},
			wantErr: false,
		},
		{
			name: "missing name",
			resource: &ResourceExpr{
				URI: "file:///test",
			},
			wantErr: true,
			errMsg:  "resource name is required",
		},
		{
			name: "missing URI",
			resource: &ResourceExpr{
				Name: "test-resource",
			},
			wantErr: true,
			errMsg:  "resource URI is required",
		},
		{
			name: "URI without scheme",
			resource: &ResourceExpr{
				Name: "test-resource",
				URI:  "not-a-uri",
			},
			wantErr: true,
			errMsg:  `resource URI "not-a-uri" must include a scheme`,
		},
		{
			name: "invalid absolute URI",
			resource: &ResourceExpr{
				Name: "test-resource",
				URI:  "file:///%zz",
			},
			wantErr: true,
			errMsg:  `resource URI "file:///%zz" is invalid`,
		},
		{
			name: "streaming method",
			resource: &ResourceExpr{
				Name: "test-resource",
				URI:  "file:///test",
				Method: &expr.MethodExpr{
					Name:   "watch",
					Stream: expr.ServerStreamKind,
				},
			},
			wantErr: true,
			errMsg:  `resource "test-resource" uses streaming method "watch"; MCP resources must return one result from one request`,
		},
		{
			name: "payload",
			resource: &ResourceExpr{
				Name:     "test-resource",
				URI:      "file:///test",
				MimeType: "application/json",
				Method: &expr.MethodExpr{
					Name: "read",
					Payload: &expr.AttributeExpr{Type: &expr.Object{
						{Name: "id", Attribute: &expr.AttributeExpr{Type: expr.String}},
					}},
					Result: &expr.AttributeExpr{Type: &expr.Object{}},
				},
			},
			wantErr: true,
			errMsg:  `resource "test-resource" method "read" must not define a payload`,
		},
		{
			name: "missing result",
			resource: &ResourceExpr{
				Name:     "test-resource",
				URI:      "file:///test",
				MimeType: "application/json",
				Method:   &expr.MethodExpr{Name: "read"},
			},
			wantErr: true,
			errMsg:  `resource "test-resource" method "read" must define a result`,
		},
		{
			name: "text resource with object result",
			resource: &ResourceExpr{
				Name:     "test-resource",
				URI:      "file:///test",
				MimeType: "text/plain",
				Method: &expr.MethodExpr{
					Name:   "read",
					Result: &expr.AttributeExpr{Type: &expr.Object{}},
				},
			},
			wantErr: true,
			errMsg:  `resource "test-resource" uses MIME type "text/plain" but method "read" does not return a string`,
		},
		{
			name: "unsupported MIME type",
			resource: &ResourceExpr{
				Name:     "test-resource",
				URI:      "file:///test",
				MimeType: "image/png",
				Method: &expr.MethodExpr{
					Name:   "read",
					Result: &expr.AttributeExpr{Type: expr.Bytes},
				},
			},
			wantErr: true,
			errMsg:  `resource "test-resource" MIME type "image/png" is not supported`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.resource.Validate()
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestPromptExpr_Validate(t *testing.T) {
	tests := []struct {
		name    string
		prompt  *PromptExpr
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid prompt",
			prompt: &PromptExpr{
				Name: "test-prompt",
				Messages: []*MessageExpr{
					{Role: "user", Content: "Hello"},
				},
			},
			wantErr: false,
		},
		{
			name: "missing name",
			prompt: &PromptExpr{
				Messages: []*MessageExpr{
					{Role: "user", Content: "Hello"},
				},
			},
			wantErr: true,
			errMsg:  "prompt name is required",
		},
		{
			name: "missing messages",
			prompt: &PromptExpr{
				Name:     "test-prompt",
				Messages: []*MessageExpr{},
			},
			wantErr: true,
			errMsg:  "prompt must have at least one message",
		},
		{
			name: "unsupported role",
			prompt: &PromptExpr{
				Name: "test-prompt",
				Messages: []*MessageExpr{
					{Role: "system", Content: "Help the user"},
				},
			},
			wantErr: true,
			errMsg:  `prompt "test-prompt" message 1 role must be "user" or "assistant"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.prompt.Validate()
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestEvalNames(t *testing.T) {
	t.Run("ToolExpr", func(t *testing.T) {
		tool := &ToolExpr{Name: "my-tool"}
		require.Equal(t, "MCP tool my-tool", tool.EvalName())
	})

	t.Run("ResourceExpr", func(t *testing.T) {
		r := &ResourceExpr{Name: "my-resource"}
		require.Equal(t, "MCP resource my-resource", r.EvalName())
	})

	t.Run("PromptExpr", func(t *testing.T) {
		p := &PromptExpr{Name: "my-prompt"}
		require.Equal(t, "MCP prompt my-prompt", p.EvalName())
	})

	t.Run("MessageExpr", func(t *testing.T) {
		m := &MessageExpr{}
		require.Equal(t, "MCP message", m.EvalName())
	})
}
