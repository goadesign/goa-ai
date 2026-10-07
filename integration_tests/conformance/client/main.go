// This executable lets the independent MCP conformance harness exercise the
// production HTTP caller. It supplies only the synthetic domain arguments the
// named harness scenario requires; the library owns all protocol messages.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"goa.design/goa-ai/runtime/mcp"

	toolcontent "goa.design/goa-ai/runtime/content"
)

func main() {
	if err := exercise(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// exercise receives the harness endpoint and calls the production client with
// the domain arguments assigned to that scenario.
// A version rejection is returned to the caller instead of trying a legacy wire.
func exercise() error {
	if len(os.Args) != 2 {
		return errors.New("conformance client requires one server URL")
	}
	if os.Getenv("MCP_CONFORMANCE_PROTOCOL_VERSION") != mcp.ProtocolVersion {
		return errors.New("conformance client requires the current protocol revision")
	}
	if strings.HasPrefix(os.Getenv("MCP_CONFORMANCE_SCENARIO"), "auth/") {
		return exerciseAuthorization(os.Args[1])
	}
	caller, err := mcp.NewHTTPCaller(mcp.HTTPOptions{
		Endpoint:   os.Args[1],
		ClientInfo: mcp.ClientInfo{Name: "goa-ai-conformance", Version: "1"},
	})
	if err != nil {
		return err
	}
	switch scenario := os.Getenv("MCP_CONFORMANCE_SCENARIO"); scenario {
	case "tools_call":
		result, err := caller.CallTool(context.Background(), mcp.CallRequest{
			Tool: "add_numbers", Payload: json.RawMessage(`{"a":17,"b":25}`),
		})
		if err != nil {
			return err
		}
		if result.InputRequired != nil || len(result.Content) != 1 {
			return errors.New("addition did not return one completed content block")
		}
		text, ok := result.Content[0].(*toolcontent.TextContent)
		if !ok || text.Text != "The sum of 17 and 25 is 42" {
			return errors.New("addition returned unexpected content")
		}
		return nil
	case "http-standard-headers":
		for _, name := range []string{"test_headers", "my-hyphenated-tool"} {
			if _, err := caller.CallTool(context.Background(), mcp.CallRequest{Tool: name}); err != nil {
				return err
			}
		}
		return nil
	case "http-custom-headers":
		calls := []mcp.CallRequest{
			{Tool: "test_custom_headers", Payload: json.RawMessage(`{"region":"us-west1","priority":42,"verbose":false,"debug":true,"empty_val":"","method_val":"test-method","float_val":3.14159,"non_ascii_val":"Hello, 世界","whitespace_val":" padded ","leading_space_val":" us-west1","trailing_space_val":"us-west1 ","internal_space_val":"us west 1","control_char_val":"line1\nline2","crlf_val":"line1\r\nline2","tab_val":"\tindented","query":"SELECT * FROM users"}`)},
			// Absence is valid for this boolean property; JSON null is outside the
			// harness's advertised argument schema. This call tests header omission.
			{Tool: "test_custom_headers_null", Payload: json.RawMessage(`{"region":"us-east1","priority":1,"query":"SELECT 1"}`)},
		}
		for _, call := range calls {
			if _, err := caller.CallTool(context.Background(), call); err != nil {
				return err
			}
		}
		return nil
	case "http-invalid-tool-headers":
		_, err := caller.CallTool(context.Background(), mcp.CallRequest{Tool: "valid_tool", Payload: json.RawMessage(`{"region":"us-west1"}`)})
		return err
	case "json-schema-ref-no-deref":
		_, err := caller.CallTool(context.Background(), mcp.CallRequest{Tool: "lookup_user", Payload: json.RawMessage(`{"id":"user-1"}`)})
		var malformed *mcp.MalformedResponseError
		if !errors.As(err, &malformed) || !strings.Contains(err.Error(), "external schema reference") {
			return fmt.Errorf("network schema reference was not rejected at compilation: %w", err)
		}
		return nil
	case "request-metadata":
		_, err := caller.CallTool(context.Background(), mcp.CallRequest{Tool: "metadata_probe"})
		var failure *mcp.Error
		if !errors.As(err, &failure) || failure.Code != mcp.UnsupportedProtocolVersion {
			return fmt.Errorf("metadata probe did not preserve the version rejection: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported conformance scenario %q", scenario)
	}
}
