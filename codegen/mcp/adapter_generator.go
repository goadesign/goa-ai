// Package codegen collects the names, schemas, and JSON conversion functions
// used to generate an MCP adapter for one Goa service.
package codegen

import (
	"encoding/json"
	"fmt"
	"mime"
	"strings"

	"goa.design/goa-ai/codegen/naming"
	"goa.design/goa-ai/codegen/shared"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

type (
	// AdapterData contains everything the MCP templates need for one Goa service.
	AdapterData struct {
		// ServiceName is the service name written in the Goa design.
		ServiceName string
		// ServiceGoName is the final Go name of the service.
		ServiceGoName string
		// MCPName is the server name returned during MCP initialization.
		MCPName string
		// MCPVersion is the server version returned during MCP initialization.
		MCPVersion string
		// ProtocolVersion is the MCP protocol version implemented by the generated
		// server.
		ProtocolVersion string
		// Package is the import name of the generated Goa service package.
		Package string
		// MCPPackage is the import name of the generated MCP service package.
		MCPPackage string
		// CodecImportPath is the private generated package that converts service
		// values to and from the JSON carried by MCP.
		CodecImportPath string
		// CodecPackage is the import name used for CodecImportPath.
		CodecPackage string
		// NeedsServerCodec reports whether the MCP server adapter calls a codec.
		NeedsServerCodec bool
		// NeedsRegisterCodec reports whether generated tool registration decodes
		// results.
		NeedsRegisterCodec bool
		// Tools contains the Goa methods exposed as MCP tools.
		Tools []*ToolAdapter
		// Resources contains the Goa methods exposed as MCP resources.
		Resources []*ResourceAdapter
		// StaticPrompts contains the prompts written directly in the Goa design.
		StaticPrompts []*StaticPromptAdapter
		// NeedsNoArgumentsValidation reports whether a tool has no payload.
		NeedsNoArgumentsValidation bool
		// NeedsBoolPtr reports that generated tool errors set MCP's optional flag.
		NeedsBoolPtr bool

		// Register contains the values used to generate agent runtime registration.
		Register *RegisterData
		// ClientSession contains the values used to generate MCP initialization.
		ClientSession *ClientSessionData
		// ClientCaller contains the values used to generate tool calls.
		ClientCaller *ClientCallerData

		mcpPackage              *codegen.GeneratedPackage
		serviceImportPath       string
		mcpImportPath           string
		mcpPathName             string
		serviceGeneratedImport  *codegen.ImportSpec
		mcpGeneratedImport      *codegen.ImportSpec
		jsonrpcClientImportPath string
		jsonrpcServerImports    *codegen.GeneratedImportPlan
		serverImportPaths       []string
		registerImportPaths     []string
		serverImports           []*codegen.ImportSpec
		registerImports         []*codegen.ImportSpec
	}

	// MethodCodecData names the generated JSON functions for one service method.
	// Empty names mean the method has no value in that direction.
	MethodCodecData struct {
		// PayloadEncode converts a service payload into MCP JSON.
		PayloadEncode string
		// PayloadDecode converts MCP JSON into a validated service payload.
		PayloadDecode string
		// ResultEncode converts a service result into MCP JSON.
		ResultEncode string
		// ResultDecode converts MCP JSON into a validated service result.
		ResultDecode string
	}

	// RegisterData drives generation of runtime registration helpers.
	RegisterData struct {
		// HelperName is the Go name shared by the generated registration helpers.
		HelperName string
		// ServiceName is the Goa service that owns the tools.
		ServiceName string
		// SuiteName is the local MCP toolset name.
		SuiteName string
		// SuiteQualifiedName identifies the toolset by its service and suite names.
		SuiteQualifiedName string
		// Description explains the generated toolset to the agent runtime.
		Description string
		// Tools contains the tools registered with the agent runtime.
		Tools []RegisterTool
	}

	// ClientCallerData contains the names and result shapes used by the generated
	// MCP caller.
	ClientCallerData struct {
		// MCPPackage is the final import name for the generated MCP service.
		MCPPackage string
		// Tools describes the statically known result contract for each tool.
		Tools []*ToolAdapter

		clientPackage     *codegen.GeneratedPackage
		clientImportPaths []string
		imports           []*codegen.ImportSpec
	}

	// ClientSessionData drives the generated MCP initialization helper.
	ClientSessionData struct {
		// MCPPackage is the final import name for the generated MCP service.
		MCPPackage string
		// JSONRPCPackage is the final import name for Goa's JSON-RPC package.
		JSONRPCPackage string
		// InitializedRequestBuilder is Goa's final request builder name for the
		// notifications/initialized method.
		InitializedRequestBuilder string
		// InitializedRequestEncoder is Goa's final request encoder name for the
		// notifications/initialized method.
		InitializedRequestEncoder string
		// HasTools reports that the generated client can call MCP tools.
		HasTools bool
		// HasResources reports that the generated client can read MCP resources.
		HasResources bool
		// HasPrompts reports that the generated client can get MCP prompts.
		HasPrompts bool

		clientPackage     *codegen.GeneratedPackage
		clientImportPaths []string
		imports           []*codegen.ImportSpec
	}

	// RegisterTool represents a single tool entry in the helper file.
	RegisterTool struct {
		// ID is the tool name sent through MCP.
		ID string
		// Title is the tool name shown to users.
		Title string
		// QualifiedName identifies the service, toolset, and tool.
		QualifiedName string
		// Description explains the tool to the model.
		Description string
		// HasPayload reports whether the Goa method accepts a payload.
		HasPayload bool
		// HasResult reports whether the Goa method returns a result.
		HasResult bool
		// HasStructuredResult reports whether MCP returns the result as structured
		// JSON.
		HasStructuredResult bool
		// TextResult reports whether MCP returns the result as plain text.
		TextResult bool
		// PayloadType is the final Go payload type.
		PayloadType string
		// ResultType is the final Go result type.
		ResultType string
		// InputSchema is the JSON Schema for tool arguments.
		InputSchema string
		// ResultSchema is the JSON Schema for the tool result.
		ResultSchema string
		// ExampleArgs is a valid JSON example for tool arguments.
		ExampleArgs string
		// Codec names the generated result decoder used after an MCP call.
		Codec *MethodCodecData
	}

	// ToolAdapter contains the generated code choices for one MCP tool.
	ToolAdapter struct {
		// Name is the tool name sent through MCP.
		Name string
		// Description explains the tool to MCP clients.
		Description string
		// ServiceMethodName is Goa's final Go name for the original service method.
		ServiceMethodName string
		// HasPayload reports whether the Goa method accepts a payload.
		HasPayload bool
		// HasResult reports whether the Goa method returns a result.
		HasResult bool
		// PayloadType is the final Go payload type.
		PayloadType string
		// ResultType is the final Go result type.
		ResultType string
		// InputSchema is the JSON Schema sent by tools/list.
		InputSchema string
		// ResultSchema is the JSON Schema used by the agent runtime for the
		// authored result type.
		ResultSchema string
		// OutputSchema is the JSON Schema for an object result. It is empty
		// when the method has no result or returns a non-object value.
		OutputSchema string
		// HasStructuredResult reports that successful calls include the
		// object result in MCP structuredContent.
		HasStructuredResult bool
		// TextResult reports that the result is a string sent as plain MCP text.
		TextResult bool
		// Codec names the functions for the original method payload and result.
		Codec *MethodCodecData
		// ExampleArguments contains a minimal valid JSON value for tool arguments.
		ExampleArguments string

		userMethodName string
	}

	// ResourceAdapter contains the generated code choices for one fixed MCP
	// resource.
	ResourceAdapter struct {
		// Name is the resource name sent through MCP.
		Name string
		// Description explains the resource to MCP clients.
		Description string
		// URI is the exact resource address accepted by resources/read.
		URI string
		// MimeType describes the resource content.
		MimeType string
		// ServiceMethodName is Goa's final Go name for the original service method.
		ServiceMethodName string
		// TextResult reports that the method result is a string returned without
		// JSON quoting because the resource declares a text MIME type.
		TextResult bool
		// Codec names the functions for the original method payload and result.
		Codec *MethodCodecData

		userMethodName string
	}

	// StaticPromptAdapter contains one prompt written directly in the Goa design.
	StaticPromptAdapter struct {
		// Name is the prompt name sent through MCP.
		Name string
		// Description explains the prompt to MCP clients.
		Description string
		// Messages is the fixed message sequence returned by prompts/get.
		Messages []*PromptMessageAdapter
	}

	// PromptMessageAdapter contains one fixed text message in a generated prompt.
	PromptMessageAdapter struct {
		// Role identifies the message author as user or assistant.
		Role string
		// Content is the message text.
		Content string
	}

	// adapterGenerator builds the values used to generate an MCP adapter for one
	// Goa service.
	adapterGenerator struct {
		originalService *expr.ServiceExpr
		mcp             *mcpexpr.MCPExpr
	}
)

const noArgumentsSchema = `{"type":"object","properties":{},"additionalProperties":false}`

// newAdapterGenerator creates a generator for one Goa service and MCP server.
func newAdapterGenerator(svc *expr.ServiceExpr, mcp *mcpexpr.MCPExpr) *adapterGenerator {
	return &adapterGenerator{
		originalService: svc,
		mcp:             mcp,
	}
}

// Private methods

// buildAdapterData creates the data for the adapter template.
func (g *adapterGenerator) buildAdapterData() (*AdapterData, error) {
	tools, err := g.buildToolAdapters()
	if err != nil {
		return nil, err
	}
	resources, err := g.buildResourceAdapters()
	if err != nil {
		return nil, err
	}
	data := &AdapterData{
		ServiceName:     g.originalService.Name,
		ServiceGoName:   codegen.Goify(g.originalService.Name, true),
		MCPName:         g.mcp.Name,
		MCPVersion:      g.mcp.Version,
		ProtocolVersion: g.mcp.ProtocolVersion,
		Package:         codegen.SnakeCase(g.originalService.Name),
		Tools:           tools,
		Resources:       resources,
		NeedsBoolPtr:    len(tools) > 0,
	}

	// Static prompts are handled directly in the adapter
	data.StaticPrompts = g.buildStaticPrompts()

	data.NeedsNoArgumentsValidation = adapterDataNeedsNoArgumentsValidation(data)

	data.Register = g.buildRegisterData(data)
	data.ClientSession = &ClientSessionData{
		HasTools:     len(data.Tools) > 0,
		HasResources: len(data.Resources) > 0,
		HasPrompts:   len(data.StaticPrompts) > 0,
	}
	data.ClientCaller = g.buildClientCallerData(data)

	return data, nil
}

// adapterDataNeedsNoArgumentsValidation reports whether generated request code
// must reject arguments for a tool or prompt that accepts no input.
func adapterDataNeedsNoArgumentsValidation(data *AdapterData) bool {
	for _, tool := range data.Tools {
		if !tool.HasPayload {
			return true
		}
	}
	return false
}

func (g *adapterGenerator) buildRegisterData(data *AdapterData) *RegisterData {
	if len(data.Tools) == 0 {
		return nil
	}
	serviceGoName := data.ServiceGoName
	suiteGoName := codegen.Goify(g.mcp.Name, true)
	desc := g.mcp.Description
	if desc == "" {
		desc = fmt.Sprintf("MCP toolset %s.%s", g.originalService.Name, g.mcp.Name)
	}
	helper := serviceGoName + suiteGoName + "Toolset"
	reg := &RegisterData{
		HelperName:         helper,
		ServiceName:        g.originalService.Name,
		SuiteName:          g.mcp.Name,
		SuiteQualifiedName: fmt.Sprintf("%s.%s", g.originalService.Name, g.mcp.Name),
		Description:        desc,
	}
	for _, tool := range data.Tools {
		payloadType := tool.PayloadType
		if payloadType == "" {
			payloadType = "any"
		}
		resultType := tool.ResultType
		if resultType == "" {
			resultType = "any"
		}
		reg.Tools = append(reg.Tools, RegisterTool{
			ID:                  tool.Name,
			Title:               naming.HumanizeTitle(tool.Name),
			QualifiedName:       fmt.Sprintf("%s.%s.%s", reg.ServiceName, reg.SuiteName, tool.Name),
			Description:         tool.Description,
			HasPayload:          tool.HasPayload,
			HasResult:           tool.HasResult,
			HasStructuredResult: tool.HasStructuredResult,
			TextResult:          tool.TextResult,
			PayloadType:         payloadType,
			ResultType:          resultType,
			InputSchema:         tool.InputSchema,
			ResultSchema:        tool.ResultSchema,
			ExampleArgs:         tool.ExampleArguments,
		})
	}
	return reg
}

func (g *adapterGenerator) buildClientCallerData(data *AdapterData) *ClientCallerData {
	if data.Register == nil {
		return nil
	}
	return &ClientCallerData{Tools: data.Tools}
}

// buildToolAdapters creates adapter data for tools.
func (g *adapterGenerator) buildToolAdapters() ([]*ToolAdapter, error) {
	adapters := make([]*ToolAdapter, 0, len(g.mcp.Tools))

	for _, tool := range g.mcp.Tools {
		// Check if payload is Empty type (added by Goa during Finalize)
		hasRealPayload := tool.Method.Payload != nil && tool.Method.Payload.Type != expr.Empty

		adapter := &ToolAdapter{
			Name:           tool.Name,
			Description:    tool.Description,
			HasPayload:     hasRealPayload,
			HasResult:      hasMCPValue(tool.Method.Result),
			userMethodName: tool.Method.Name,
		}

		// Set payload type reference only for real payloads
		if hasRealPayload {
			// Generate a minimal JSON Schema for MCP tools/list
			schema, err := shared.ToJSONSchema(tool.Method.Payload)
			if err != nil {
				return nil, fmt.Errorf("build schema for tool %q: %w", tool.Name, err)
			}
			adapter.InputSchema = schema
			// Produce a minimal valid example JSON for arguments.
			example, err := g.buildExampleJSON(tool.Method)
			if err != nil {
				return nil, fmt.Errorf("build example for tool %q: %w", tool.Name, err)
			}
			adapter.ExampleArguments = example
		} else {
			adapter.InputSchema = noArgumentsSchema
			adapter.ExampleArguments = "{}"
		}
		if adapter.HasResult {
			schema, err := shared.ToJSONSchema(tool.Method.Result)
			if err != nil {
				return nil, fmt.Errorf("build output schema for tool %q: %w", tool.Name, err)
			}
			adapter.ResultSchema = schema
			if expr.AsObject(tool.Method.Result.Type) != nil {
				adapter.OutputSchema = schema
				adapter.HasStructuredResult = true
			}
			adapter.TextResult = shared.IsStringType(tool.Method.Result.Type)
		}

		adapters = append(adapters, adapter)
	}

	return adapters, nil
}

// buildResourceAdapters creates adapter data for resources.
func (g *adapterGenerator) buildResourceAdapters() ([]*ResourceAdapter, error) {
	adapters := make([]*ResourceAdapter, 0, len(g.mcp.Resources))

	for _, resource := range g.mcp.Resources {
		mediaType, _, err := mime.ParseMediaType(resource.MimeType)
		if err != nil {
			return nil, fmt.Errorf("parse MIME type for resource %q: %w", resource.Name, err)
		}
		adapter := &ResourceAdapter{
			Name:           resource.Name,
			Description:    resource.Description,
			URI:            resource.URI,
			MimeType:       resource.MimeType,
			TextResult:     strings.HasPrefix(mediaType, "text/"),
			userMethodName: resource.Method.Name,
		}

		adapters = append(adapters, adapter)
	}

	return adapters, nil
}

// hasMCPValue reports whether a method side carries application data.
func hasMCPValue(attribute *expr.AttributeExpr) bool {
	return attribute != nil && attribute.Type != nil && attribute.Type != expr.Empty
}

// buildExampleJSON returns a repeatable JSON example for a method payload.
func (g *adapterGenerator) buildExampleJSON(method *expr.MethodExpr) (string, error) {
	attr := method.Payload
	if attr == nil || attr.Type == nil || attr.Type == expr.Empty {
		return "{}", nil
	}
	r := expr.NewExampleGenerator(expr.NewDeterministicRandomizerFactory()).At(
		expr.MethodPayloadExampleIdentity(method),
	)
	v := attr.Example(r)
	if v == nil {
		return "", fmt.Errorf("method %q did not produce a payload example", method.Name)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode method %q payload example: %w", method.Name, err)
	}
	return string(b), nil
}

// buildStaticPrompts creates data for static prompts
func (g *adapterGenerator) buildStaticPrompts() []*StaticPromptAdapter {
	prompts := make([]*StaticPromptAdapter, 0, len(g.mcp.Prompts))

	for _, prompt := range g.mcp.Prompts {
		adapter := &StaticPromptAdapter{
			Name:        prompt.Name,
			Description: prompt.Description,
			Messages:    make([]*PromptMessageAdapter, len(prompt.Messages)),
		}

		for i, msg := range prompt.Messages {
			adapter.Messages[i] = &PromptMessageAdapter{
				Role:    msg.Role,
				Content: msg.Content,
			}
		}

		prompts = append(prompts, adapter)
	}

	return prompts
}
