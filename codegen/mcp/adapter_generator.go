// Package codegen collects the names, schemas, and JSON conversion functions
// used to generate an MCP adapter for one Goa service.
package codegen

import (
	"encoding/json"
	"fmt"
	"mime"
	"sort"
	"strings"

	"goa.design/goa-ai/codegen/internal/jsonschema"
	"goa.design/goa-ai/codegen/internal/mcpcontract"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa-ai/internal/mcpprotocol"
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
		// ResultMeta is the generated server identity encoded as protocol metadata.
		ResultMeta string
		// MCPName is the server name returned in MCP response metadata.
		MCPName string
		// MCPVersion is the server version returned in MCP response metadata.
		MCPVersion string
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
		// EndpointsName is Goa's final name for the configured endpoint collection.
		EndpointsName string
		// NeedsEndpointResultCheck reports whether endpoints return typed results.
		NeedsEndpointResultCheck bool
		// EndpointMethods contains one typed call per authored MCP method.
		EndpointMethods []*endpointMethodAdapter
		// Tools contains the Goa methods exposed as MCP tools.
		Tools []*ToolAdapter
		// Resources contains the Goa methods exposed as MCP resources.
		Resources []*ResourceAdapter
		// ResourceTemplates contains the advertised parameterized addresses.
		ResourceTemplates []*resourceTemplateAdapter
		// ResourceReader owns reads that are not fixed resource bindings.
		ResourceReader *resourceReaderAdapter
		// StaticPrompts contains the prompts written directly in the Goa design.
		StaticPrompts []*StaticPromptAdapter
		// MethodPrompts contains prompt operations implemented by service methods.
		MethodPrompts []*MethodPromptAdapter
		// CompletionReferences contains the argument names accepted by each reference.
		CompletionReferences []*completionReferenceAdapter
		// Completions contains typed providers for known prompt and resource arguments.
		Completions []*completionAdapter
		// ContentConversions contains the shared generated content and resource converters.
		ContentConversions []*contentConversionData
		// NeedsContentBytes reports that authored content includes binary bytes.
		NeedsContentBytes bool
		// NeedsContentMeta reports that authored metadata needs object validation.
		NeedsContentMeta bool
		// NeedsContentNumbers reports that content needs finite JSON number checks.
		NeedsContentNumbers bool
		// NeedsNoArgumentsValidation reports whether a tool has no payload.
		NeedsNoArgumentsValidation bool
		// NeedsBoolPtr reports that generated tool errors set MCP's optional flag.
		NeedsBoolPtr bool

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
		jsonrpcClientImports    *codegen.GeneratedImportPlan
		serverImportPaths       []string
		serverImports           []*codegen.ImportSpec
	}

	// MethodCodecData names the generated JSON functions for one service method.
	// Empty names mean the method has no value in that direction.
	MethodCodecData struct {
		// PayloadDecode validates domain JSON and constructs the original typed payload.
		PayloadDecode string
		// ResultEncode converts a service result into MCP JSON.
		ResultEncode string
		// ResultViews records the declared execution-selected views.
		ResultViews []*resultViewCodec
		// ResultValidate checks the typed result before MCP conversion.
		ResultValidate string
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

	// ToolAdapter contains the generated code choices for one MCP tool.
	ToolAdapter struct {
		// Name is the tool name sent through MCP.
		Name string
		// Description explains the tool to MCP clients.
		Description string
		// Annotations contains the behavior hints advertised by this tool.
		Annotations *mcpexpr.ToolAnnotationsExpr
		// ReadOnly is the design-time promise used by the generated HTTP binding.
		ReadOnly bool
		// Idempotent is the design-time promise that repeated arguments have no additional effects.
		Idempotent bool
		// Endpoint calls the configured Goa endpoint for this method.
		Endpoint *endpointMethodAdapter
		// HasPayload reports whether the Goa method accepts a payload.
		HasPayload bool
		// HasResult reports whether the Goa method returns a result.
		HasResult bool
		// Content converts the authored content field for the selected view.
		Content *toolContentAdapter
		// InputSchema is the JSON Schema sent by tools/list.
		InputSchema string
		// Headers contains precomputed paths mirrored to HTTP headers.
		Headers []mcpprotocol.HeaderBinding
		// ResultSchema is the JSON Schema used by the agent runtime for the
		// authored result type.
		ResultSchema string
		// OutputSchema describes the structured fields, including scalar and array roots.
		// It is empty when the tool returns only content or no result.
		OutputSchema string
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
		// Endpoint calls the configured Goa endpoint for this method.
		Endpoint *endpointMethodAdapter
		// TextResult reports that the method result is a string returned without
		// JSON quoting because the resource declares a text MIME type.
		TextResult bool
		// BinaryResult selects base64 blob content for a byte-valued service result.
		BinaryResult bool
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
		api             *expr.APIExpr
		originalService *expr.ServiceExpr
		mcp             *mcpexpr.MCPExpr
	}
)

const noArgumentsSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{},"additionalProperties":false}`

// newAdapterGenerator creates a generator for one Goa service and MCP server.
func newAdapterGenerator(api *expr.APIExpr, svc *expr.ServiceExpr, mcp *mcpexpr.MCPExpr) *adapterGenerator {
	return &adapterGenerator{
		api:             api,
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
	prompts, err := g.buildMethodPromptAdapters()
	if err != nil {
		return nil, err
	}
	templates, err := buildResourceTemplateAdapters(g.mcp.ResourceTemplates)
	if err != nil {
		return nil, err
	}
	reader, err := g.buildResourceReaderAdapter()
	if err != nil {
		return nil, err
	}
	completions, err := g.buildCompletionAdapters()
	if err != nil {
		return nil, err
	}
	references, err := g.buildCompletionReferences(templates)
	if err != nil {
		return nil, err
	}
	data := &AdapterData{
		ServiceName:          g.originalService.Name,
		ServiceGoName:        codegen.Goify(g.originalService.Name, true),
		MCPName:              g.mcp.Name,
		MCPVersion:           g.mcp.Version,
		Package:              codegen.SnakeCase(g.originalService.Name),
		Tools:                tools,
		Resources:            resources,
		ResourceTemplates:    templates,
		ResourceReader:       reader,
		MethodPrompts:        prompts,
		Completions:          completions,
		CompletionReferences: references,
		NeedsBoolPtr:         len(tools)+len(prompts) > 0,
	}

	// Static prompts are handled directly in the adapter
	data.StaticPrompts = g.buildStaticPrompts()

	metadata, err := json.Marshal(map[string]any{"io.modelcontextprotocol/serverInfo": map[string]string{"name": g.mcp.Name, "version": g.mcp.Version}})
	if err != nil {
		return nil, fmt.Errorf("encode MCP server metadata: %w", err)
	}
	data.ResultMeta = string(metadata)
	data.NeedsNoArgumentsValidation = adapterDataNeedsNoArgumentsValidation(data)

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

func (g *adapterGenerator) buildClientCallerData(data *AdapterData) *ClientCallerData {
	if len(data.Tools) == 0 {
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
			Annotations:    tool.Annotations,
			HasPayload:     hasRealPayload,
			HasResult:      hasMCPValue(tool.Method.Result),
			userMethodName: tool.Method.Name,
		}

		if tool.Annotations != nil {
			adapter.ReadOnly = tool.Annotations.ReadOnlyHint != nil && *tool.Annotations.ReadOnlyHint
			adapter.Idempotent = tool.Annotations.IdempotentHint != nil && *tool.Annotations.IdempotentHint
		}

		arguments, err := mcpinput.Arguments(tool.Method.Payload)
		if err != nil {
			return nil, fmt.Errorf("tool %q arguments: %w", tool.Name, err)
		}

		// Set payload type reference only for real payloads
		if hasRealPayload {
			// Generate a minimal JSON Schema for MCP tools/list
			schema, err := jsonschema.Build(g.api, arguments, expr.MethodPayloadExampleIdentity(tool.Method))
			if err != nil {
				return nil, fmt.Errorf("build schema for tool %q: %w", tool.Name, err)
			}
			adapter.InputSchema = string(schema)
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
		headers, err := mcpprotocol.CompileHeaderBindings(json.RawMessage(adapter.InputSchema))
		if err != nil {
			return nil, fmt.Errorf("tool %q header annotations: %w", tool.Name, err)
		}
		adapter.Headers = headers
		if tool.ContentField != "" {
			content, err := g.buildToolContentAdapter(tool)
			if err != nil {
				return nil, err
			}
			adapter.Content = content
		}
		if adapter.HasResult {
			result, err := mcpcontract.ToolResult(tool)
			if err != nil {
				return nil, err
			}
			if !hasMCPValue(result) {
				adapters = append(adapters, adapter)
				continue
			}
			schema, err := jsonschema.Build(g.api, result, expr.MethodResultExampleIdentity(tool.Method))
			if err != nil {
				return nil, fmt.Errorf("build output schema for tool %q: %w", tool.Name, err)
			}
			adapter.ResultSchema = string(schema)
			adapter.OutputSchema = string(schema)
		}

		adapters = append(adapters, adapter)
	}

	sort.Slice(adapters, func(i, j int) bool { return adapters[i].Name < adapters[j].Name })
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
		resultType := resource.Method.Result.Type
		for {
			named, ok := resultType.(expr.UserType)
			if !ok {
				break
			}
			resultType = named.Attribute().Type
		}
		adapter := &ResourceAdapter{
			Name:           resource.Name,
			Description:    resource.Description,
			URI:            resource.URI,
			MimeType:       resource.MimeType,
			TextResult:     strings.HasPrefix(mediaType, "text/") && resultType != expr.Bytes,
			BinaryResult:   resultType == expr.Bytes,
			userMethodName: resource.Method.Name,
		}

		adapters = append(adapters, adapter)
	}

	sort.Slice(adapters, func(i, j int) bool { return adapters[i].URI < adapters[j].URI })
	return adapters, nil
}

// hasMCPValue reports whether a method side carries application data.
func hasMCPValue(attribute *expr.AttributeExpr) bool {
	return attribute != nil && attribute.Type != nil && attribute.Type != expr.Empty
}

// buildExampleJSON returns a repeatable JSON example for a method payload.
func (g *adapterGenerator) buildExampleJSON(method *expr.MethodExpr) (string, error) {
	attr, err := mcpinput.Arguments(method.Payload)
	if err != nil {
		return "", err
	}
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

	sort.Slice(prompts, func(i, j int) bool { return prompts[i].Name < prompts[j].Name })
	return prompts
}
