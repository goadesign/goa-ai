// Package tests compiles generated MCP executors against the runtime contract.
package tests

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// TestGeneratedMCPExecutorCompiles covers every supported MCP tool result
// shape so stale runtime response fields and conditional imports fail here.
func TestGeneratedMCPExecutorCompiles(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.MCPUse())
	runCompleteGeneratedPackageTest(t, files, "./gen/alpha/agents/scribe/core")
}

func TestGeneratedMCPExecutorDecodesEveryStringControlCharacter(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.MCPUse())
	root := writeCompleteGeneratedModule(t, files)
	const testSource = `package core

import (
	"context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
	"testing"

	gencalccore "generated.local/gen/calc/toolsets/core"
	"goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/content"
	"reflect"
	mcpruntime "goa.design/goa-ai/runtime/mcp"
)

func TestStringResultControlCharacters(t *testing.T) {
	want := "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f" +
		"\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f"
	caller := mcpruntime.CallerFunc(func(context.Context, mcpruntime.CallRequest) (mcpruntime.CallResponse, error) {
		encoded, err := json.Marshal(want)
        return mcpruntime.CallResponse{StructuredContent: encoded}, err
	})
	executor := NewMCPExecutor(caller)
	result, err := executor.Execute(context.Background(), &runtime.ToolCallMeta{}, &runtime.ToolCall{
		Name:    gencalccore.Label,
		Payload: []byte(` + "`{}`" + `),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ToolResult.Failure != nil {
		t.Fatalf("string result failed: %s", result.ToolResult.Failure.Error.Message)
	}
	got, ok := result.ToolResult.Result.(string)
	if !ok {
		t.Fatalf("string result has type %T", result.ToolResult.Result)
	}
	if got != want {
		t.Fatalf("string result = %q, want %q", got, want)
	}
}
func TestMCPExecutorRestrictsHostInputPerCall(t *testing.T) {
    advertised := make(chan bool, 3)
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        var request struct {
            ID json.RawMessage "json:\"id\""
            Params struct {
                Meta map[string]json.RawMessage "json:\"_meta\""
            } "json:\"params\""
        }
        if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
            t.Error(err)
            return
        }
        var capabilities struct {
            Elicitation map[string]json.RawMessage "json:\"elicitation\""
        }
        if err := json.Unmarshal(request.Params.Meta["io.modelcontextprotocol/clientCapabilities"], &capabilities); err != nil {
            t.Error(err)
            return
        }
        advertised <- len(capabilities.Elicitation) == 2
        w.Header().Set("Content-Type", "application/json")
        if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc":"2.0", "id":request.ID, "result":map[string]any{"resultType":"complete", "content":[]any{}, "structuredContent":"done"}}); err != nil {
            t.Error(err)
        }
    }))
    defer server.Close()
    transport := mcpruntime.NewHTTPTransport(server.Client(), mcpruntime.ClientInfo{Name:"generated-test",Version:"1"}, mcpruntime.HTTPBindings{}, mcpruntime.InputSupport{Form:true,URL:true}, mcpruntime.HTTPRetryPolicy{})
    caller := mcpruntime.CallerFunc(func(ctx context.Context, request mcpruntime.CallRequest) (mcpruntime.CallResponse, error) {
        return transport.CallTool(ctx, server.URL, request)
    })
    executor := NewMCPExecutor(caller)
    for _, textOnly := range []bool{false, true, false} {
        result, err := executor.Execute(t.Context(), &runtime.ToolCallMeta{}, &runtime.ToolCall{Name:gencalccore.Label, Payload:[]byte(` + "`{}`" + `), TextOnly:textOnly})
        if err != nil {
            t.Fatal(err)
        }
        if result.ToolResult.Failure != nil {
            t.Fatalf("execution failed: %s", result.ToolResult.Failure.Error.Message)
        }
        if supported := <-advertised; supported == textOnly {
            t.Fatalf("textOnly=%t advertised host input=%t", textOnly, supported)
        }
    }
}

func TestMCPExecutorRetainsOrderedContent(t *testing.T) {
    blocks := content.Blocks{
        &content.TextContent{Text:"returned",Meta:json.RawMessage(` + "`" + `{"id":9007199254740993}` + "`" + `)},
        &content.ImageContent{Data:"AQID",MIMEType:"image/png"},
        &content.AudioContent{Data:"BAUG",MIMEType:"audio/wav"},
        &content.ResourceLink{Name:"report",URI:"https://example.com/report"},
        &content.EmbeddedResource{Resource:&content.TextResourceContents{URI:"report://inline",Text:"complete"}},
    }
    for _, test := range []struct {
        name string
        tool tools.Ident
        structured json.RawMessage
        failed bool
        malformed bool
        invalidContent bool
    }{
        {name:"typed plus content",tool:gencalccore.Label,structured:json.RawMessage(` + "`" + `"accepted"` + "`" + `)},
        {name:"content only",tool:gencalccore.Reset},
        {name:"failed with content",tool:gencalccore.Label,failed:true},
        {name:"typed result still required",tool:gencalccore.Label,malformed:true},
        {name:"invalid content",tool:gencalccore.Reset,malformed:true,invalidContent:true},
        {name:"invalid failed content",tool:gencalccore.Label,failed:true,malformed:true,invalidContent:true},
        {name:"no result rejects structured",tool:gencalccore.Reset,structured:json.RawMessage(` + "`" + `{}` + "`" + `),malformed:true},
    } {
        t.Run(test.name,func(t *testing.T) {
            caller := mcpruntime.CallerFunc(func(context.Context,mcpruntime.CallRequest)(mcpruntime.CallResponse,error){
                response:=mcpruntime.CallResponse{Content:blocks,StructuredContent:test.structured}
                if test.invalidContent { response.Content=content.Blocks{&content.ImageContent{Data:"%%%",MIMEType:"image/png"}} }
                if test.failed { return mcpruntime.CallResponse{},&mcpruntime.ToolExecutionError{Response:response} }
                return response,nil
            })
            result,err:=NewMCPExecutor(caller).Execute(t.Context(),&runtime.ToolCallMeta{},&runtime.ToolCall{Name:test.tool,Payload:[]byte(` + "`" + `{}` + "`" + `)})
            if err!=nil { t.Fatal(err) }
            if test.malformed {
                if result.ToolResult.Failure==nil || result.ToolResult.Failure.Kind!=planner.FailureMalformedResult { t.Fatal("malformed result accepted") }
                return
            }
            if !reflect.DeepEqual(blocks,result.ToolResult.Blocks) { t.Fatalf("content changed: %#v",result.ToolResult.Blocks) }
            if (result.ToolResult.Failure!=nil)!=test.failed { t.Fatal("failure status changed") }
            result.ToolResult.Blocks[0].(*content.TextContent).Text="changed"
            if blocks[0].(*content.TextContent).Text!="returned" { t.Fatal("caller content is shared") }
        })
    }
}

`
	testPath := filepath.Join(root, "gen", "alpha", "agents", "scribe", "core", "mcp_executor_runtime_test.go")
	require.NoError(t, os.WriteFile(testPath, []byte(testSource), 0o600))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-mod=mod", "./gen/alpha/agents/scribe/core")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	require.NoErrorf(t, command.Run(), "go test failed:\n%s", output.String())
}
