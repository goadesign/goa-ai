package codegen

import (
	"encoding/json"
	"fmt"
	"sort"

	"goa.design/goa-ai/codegen/naming"
	"goa.design/goa-ai/codegen/shared"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// AdapterData holds the data for generating the adapter
	AdapterData struct {
		ServiceName     string
		ServiceGoName   string
		MCPName         string
		MCPVersion      string
		ProtocolVersion string
		Package         string
		MCPPackage      string
		// CodecImportPath is the private generated package that converts service
		// values to and from the JSON carried by MCP.
		CodecImportPath string
		// CodecPackage is the import name used for CodecImportPath.
		CodecPackage string
		// NeedsServerCodec reports whether the MCP server adapter calls a codec.
		NeedsServerCodec bool
		// NeedsClientCodec reports whether the MCP client adapter calls a codec.
		NeedsClientCodec bool
		Tools            []*ToolAdapter
		Resources        []*ResourceAdapter
		StaticPrompts    []*StaticPromptAdapter
		DynamicPrompts   []*DynamicPromptAdapter
		Notifications    []*NotificationAdapter
		// Derived flags
		HasWatchableResources bool
		NeedsMCPClient        bool
		NeedsQueryFormatting  bool
		// NeedsNoArgumentsValidation reports whether a tool or prompt has no payload.
		NeedsNoArgumentsValidation bool

		Register     *RegisterData
		ClientCaller *ClientCallerData

		mcpPackage                *codegen.GeneratedPackage
		serviceImportPath         string
		mcpImportPath             string
		mcpPathName               string
		serviceGeneratedImport    *codegen.ImportSpec
		mcpGeneratedImport        *codegen.ImportSpec
		jsonrpcClientImportPath   string
		serverImportPaths         []string
		promptProviderImportPaths []string
		registerImportPaths       []string
		serverImports             []*codegen.ImportSpec
		promptProviderImports     []*codegen.ImportSpec
		registerImports           []*codegen.ImportSpec
		clientPackagePath         string
		clientPackage             *codegen.GeneratedPackage
		clientImportPaths         []string
		clientPayloadImportPaths  map[string]struct{}
		clientImports             []*codegen.ImportSpec
		clientMethodNames         []string
		clientPackageName         string
		clientServicePackage      string
		clientMCPPackage          string
		clientJSONRPCPackage      string
		clientCodecPackage        string
	}

	// MethodCodecData names the generated JSON functions for one service method.
	// Empty names mean the method has no value in that direction.
	MethodCodecData struct {
		// PayloadEncode converts a service payload into MCP JSON.
		PayloadEncode string
		// PayloadDecode converts MCP JSON into a validated service payload.
		PayloadDecode string
		// PayloadNew validates a filled private transport value and returns a service payload.
		PayloadNew string
		// PayloadTransport is the private transport type filled by a generated server adapter.
		PayloadTransport string
		// ResultEncode converts a service result into MCP JSON.
		ResultEncode string
		// ResultDecode converts MCP JSON into a validated service result.
		ResultDecode string
	}

	// RegisterData drives generation of runtime registration helpers.
	RegisterData struct {
		HelperName         string
		ServiceName        string
		SuiteName          string
		SuiteQualifiedName string
		Description        string
		Tools              []RegisterTool
	}

	ClientCallerData struct {
		// MCPPackage is the final import name for the generated MCP service.
		MCPPackage string
		// JSONRPCPackage is the final import name for Goa's JSON-RPC package.
		JSONRPCPackage string

		clientPackage     *codegen.GeneratedPackage
		clientImportPaths []string
		imports           []*codegen.ImportSpec
	}

	// RegisterTool represents a single tool entry in the helper file.
	RegisterTool struct {
		ID            string
		Title         string
		QualifiedName string
		Description   string
		PayloadType   string
		ResultType    string
		InputSchema   string
		ExampleArgs   string
	}

	// ToolAdapter represents a tool adapter
	ToolAdapter struct {
		Name        string
		Description string
		// ServiceMethodName is Goa's final Go name for the original service method.
		ServiceMethodName string
		HasPayload        bool
		HasResult         bool
		PayloadType       string
		ResultType        string
		InputSchema       string
		// Codec names the functions for the original method payload and result.
		Codec *MethodCodecData
		// ExampleArguments contains a minimal valid JSON for tool arguments
		ExampleArguments string

		userMethodName string
	}

	// ResourceAdapter represents a resource adapter
	ResourceAdapter struct {
		Name        string
		Description string
		URI         string
		MimeType    string
		// ServiceMethodName is Goa's final Go name for the original service method.
		ServiceMethodName string
		HasPayload        bool
		HasResult         bool
		PayloadType       string
		ResultType        string
		QueryFields       []*ResourceQueryField
		Watchable         bool
		// Codec names the functions for the original method payload and result.
		Codec *MethodCodecData

		userMethodName string
	}

	// ResourceQueryField describes one statically known query parameter binding
	// for a resource payload field.
	ResourceQueryField struct {
		// QueryKey is the field name written in the resource URI.
		QueryKey string
		// ClientSelector is the exact Go field name chosen by Goa for the service payload.
		ClientSelector string
		// FormatKind selects the direct primitive-to-string conversion to emit.
		FormatKind string
		// ValueType is the Go primitive type used to parse this query field.
		ValueType string
		// ParseBitSize limits numeric input to the range accepted by ValueType.
		ParseBitSize string
		// Repeated reports that each array value is written as a separate query value.
		Repeated bool
		// ClientPointer reports that the service payload stores this value through a pointer.
		ClientPointer bool
		// Optional reports that the service method does not require this field.
		Optional bool
		// TransportSelector is the exact field name in the private JSON type.
		TransportSelector string
		// TransportType is the complete private JSON field type.
		TransportType string
		// TransportValueType is the private JSON field type without its presence pointer.
		TransportValueType string
		// TransportPointer reports that the private JSON field stores its value through a pointer.
		TransportPointer bool
		// TransportElementType is the private element type for an array field.
		TransportElementType string
		// TransportElementPointer reports that an array stores each value through a pointer.
		TransportElementPointer bool

		attribute *expr.AttributeExpr
	}

	// resourceQueryFieldDefinition captures one flattened top-level resource
	// query field together with the presence rules implied by the Goa payload.
	resourceQueryFieldDefinition struct {
		Attribute *expr.AttributeExpr
		Required  bool
	}

	// StaticPromptAdapter represents a static prompt
	StaticPromptAdapter struct {
		Name        string
		Description string
		Messages    []*PromptMessageAdapter
		// ProviderMethodName is the final method name on PromptProvider.
		ProviderMethodName string
	}

	// PromptMessageAdapter represents a prompt message
	PromptMessageAdapter struct {
		Role    string
		Content string
	}

	// DynamicPromptAdapter represents a dynamic prompt adapter
	DynamicPromptAdapter struct {
		Name        string
		Description string
		// ProviderMethodName is the final method name on PromptProvider.
		ProviderMethodName string
		// ServiceMethodName is Goa's final Go name for the original service method.
		ServiceMethodName string
		HasPayload        bool
		PayloadType       string
		ResultType        string
		// Codec names the functions for the original method payload and result.
		Codec *MethodCodecData
		// Arguments describes prompt arguments derived from the payload (dynamic prompts)
		Arguments []PromptArg
		// ExampleArguments contains a minimal valid JSON for prompt arguments
		ExampleArguments string

		userMethodName string
	}

	// PromptArg is a lightweight representation for generating PromptArgument values
	PromptArg struct {
		Name        string
		Description string
		Required    bool
	}

	// NotificationAdapter represents a notification mapping
	NotificationAdapter struct {
		Name        string
		Description string
		// ServiceMethodName is Goa's final Go name for the original service method.
		ServiceMethodName string
		// PayloadType is the exact original service payload type.
		PayloadType string
		// MCPMethodName is the final Go method name in the generated MCP service.
		MCPMethodName string
		// WireMethodName is the JSON-RPC method sent over the transport.
		WireMethodName string
		// RequestBuilderName is the final JSON-RPC request builder name.
		RequestBuilderName string
		// ResponseDecoderName is the final JSON-RPC response decoder name.
		ResponseDecoderName string
		// PayloadRef is the final generated MCP request payload type.
		PayloadRef string
		// Codec names the functions for the original method payload.
		Codec *MethodCodecData

		userMethodName string
	}

	// adapterGenerator generates the adapter layer between MCP and the original service
	adapterGenerator struct {
		originalService *expr.ServiceExpr
		mcp             *mcpexpr.MCPExpr
		dynamicPrompts  []*mcpexpr.DynamicPromptExpr
		mapping         *ServiceMethodMapping
		scope           *codegen.NameScope
	}
)

const (
	resourceQueryFormatString  = "string"
	resourceQueryFormatBool    = "bool"
	resourceQueryFormatInt     = "int"
	resourceQueryFormatUint    = "uint"
	resourceQueryFormatFloat32 = "float32"
	resourceQueryFormatFloat64 = "float64"
)

// newAdapterGenerator creates a new adapter generator
func newAdapterGenerator(
	svc *expr.ServiceExpr,
	mcp *mcpexpr.MCPExpr,
	dynamicPrompts []*mcpexpr.DynamicPromptExpr,
	mapping *ServiceMethodMapping,
) *adapterGenerator {
	return &adapterGenerator{
		originalService: svc,
		mcp:             mcp,
		dynamicPrompts:  dynamicPrompts,
		mapping:         mapping,
		scope:           codegen.NewNameScope(),
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
		DynamicPrompts:  g.buildDynamicPromptAdapters(),
		Notifications:   g.buildNotificationAdapters(),
	}

	// Static prompts are handled directly in the adapter
	data.StaticPrompts = g.buildStaticPrompts()
	planPromptProviderMethodNames(data.StaticPrompts, data.DynamicPrompts)

	// Derive watchable resources presence
	for _, r := range data.Resources {
		if r.Watchable {
			data.HasWatchableResources = true
			break
		}
	}
	data.NeedsMCPClient = len(data.Tools) > 0 ||
		len(data.Resources) > 0 ||
		len(data.DynamicPrompts) > 0 ||
		len(data.Notifications) > 0
	data.NeedsQueryFormatting = adapterDataNeedsQueryFormatting(data.Resources)
	data.NeedsNoArgumentsValidation = adapterDataNeedsNoArgumentsValidation(data)

	data.Register = g.buildRegisterData(data)
	data.ClientCaller = g.buildClientCallerData(data)

	return data, nil
}

// planPromptProviderMethodNames chooses one stable Go method name for every
// prompt provider definition and call.
func planPromptProviderMethodNames(static []*StaticPromptAdapter, dynamic []*DynamicPromptAdapter) {
	type promptMethod struct {
		name    string
		kind    string
		setName func(string)
	}
	methods := make([]promptMethod, 0, len(static)+len(dynamic))
	for _, prompt := range static {
		methods = append(methods, promptMethod{
			name: prompt.Name,
			kind: "Static",
			setName: func(name string) {
				prompt.ProviderMethodName = name
			},
		})
	}
	for _, prompt := range dynamic {
		methods = append(methods, promptMethod{
			name: prompt.Name,
			kind: "Dynamic",
			setName: func(name string) {
				prompt.ProviderMethodName = name
			},
		})
	}
	sort.Slice(methods, func(i, j int) bool {
		if methods[i].name != methods[j].name {
			return methods[i].name < methods[j].name
		}
		return methods[i].kind < methods[j].kind
	})
	scope := codegen.NewNameScope()
	for _, method := range methods {
		preferred := "Get" + codegen.Goify(method.name, true) + "Prompt"
		method.setName(scope.Unique(preferred, method.kind))
	}
}

// adapterDataNeedsNoArgumentsValidation reports whether generated request code
// must reject arguments for a tool or prompt that accepts no input.
func adapterDataNeedsNoArgumentsValidation(data *AdapterData) bool {
	if len(data.StaticPrompts) > 0 {
		return true
	}
	for _, tool := range data.Tools {
		if !tool.HasPayload {
			return true
		}
	}
	for _, prompt := range data.DynamicPrompts {
		if !prompt.HasPayload {
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
		schema := tool.InputSchema
		if schema == "" {
			schema = "{}"
		}
		payloadType := tool.PayloadType
		if payloadType == "" {
			payloadType = "any"
		}
		resultType := tool.ResultType
		if resultType == "" {
			resultType = "any"
		}
		reg.Tools = append(reg.Tools, RegisterTool{
			ID:            tool.Name,
			Title:         naming.HumanizeTitle(tool.Name),
			QualifiedName: fmt.Sprintf("%s.%s.%s", reg.ServiceName, reg.SuiteName, tool.Name),
			Description:   tool.Description,
			PayloadType:   payloadType,
			ResultType:    resultType,
			InputSchema:   schema,
			ExampleArgs:   tool.ExampleArguments,
		})
	}
	return reg
}

func (g *adapterGenerator) buildClientCallerData(data *AdapterData) *ClientCallerData {
	if data.Register == nil {
		return nil
	}
	return new(ClientCallerData)
}

// adapterDataNeedsQueryFormatting reports whether resource query emission needs
// strconv-based formatting for non-string primitive query values.
func adapterDataNeedsQueryFormatting(resources []*ResourceAdapter) bool {
	for _, resource := range resources {
		for _, field := range resource.QueryFields {
			if field.FormatKind != resourceQueryFormatString {
				return true
			}
		}
	}
	return false
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
			// Produce a minimal valid example JSON for arguments
			adapter.ExampleArguments = g.buildExampleJSON(tool.Method)
		} else {
			adapter.ExampleArguments = "{}"
		}

		// Set result type reference
		if hasMCPValue(tool.Method.Result) {
			adapter.ResultType = g.getResultTypeReference(tool.Method.Result)
		}

		adapters = append(adapters, adapter)
	}

	return adapters, nil
}

// buildResourceAdapters creates adapter data for resources.
func (g *adapterGenerator) buildResourceAdapters() ([]*ResourceAdapter, error) {
	adapters := make([]*ResourceAdapter, 0, len(g.mcp.Resources))

	for _, resource := range g.mcp.Resources {
		// Check if payload is Empty type (added by Goa during Finalize)
		hasRealPayload := resource.Method.Payload != nil && resource.Method.Payload.Type != expr.Empty

		adapter := &ResourceAdapter{
			Name:           resource.Name,
			Description:    resource.Description,
			URI:            resource.URI,
			MimeType:       resource.MimeType,
			HasPayload:     hasRealPayload,
			HasResult:      hasMCPValue(resource.Method.Result),
			Watchable:      resource.Watchable,
			userMethodName: resource.Method.Name,
		}

		// Set payload type reference only for real payloads
		if hasRealPayload {
			queryFields, err := buildResourceQueryFields(resource.Method.Payload)
			if err != nil {
				return nil, fmt.Errorf("build resource query fields for %q: %w", resource.Method.Name, err)
			}
			adapter.QueryFields = queryFields
		}

		// Set result type reference
		if hasMCPValue(resource.Method.Result) {
			adapter.ResultType = g.getResultTypeReference(resource.Method.Result)
		}

		adapters = append(adapters, adapter)
	}

	return adapters, nil
}

// buildResourceQueryFields computes the statically known resource query plan so
// the template can emit direct query assembly without rediscovering payload
// structure at runtime.
func buildResourceQueryFields(payload *expr.AttributeExpr) ([]*ResourceQueryField, error) {
	definitions := make(map[string]resourceQueryFieldDefinition)
	collectResourceQueryFields(payload, payload, definitions, make(map[string]struct{}))
	if len(definitions) == 0 {
		return nil, fmt.Errorf(
			"payload must define at least one top-level primitive or array-of-primitive query field",
		)
	}
	names := make([]string, 0, len(definitions))
	for name := range definitions {
		names = append(names, name)
	}
	sort.Strings(names)

	fields := make([]*ResourceQueryField, 0, len(names))
	for _, name := range names {
		field, err := newResourceQueryField(name, definitions[name])
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	return fields, nil
}

// collectResourceQueryFields flattens the top-level resource payload across
// direct fields, bases, and references so generated query assembly preserves
// the original payload surface without runtime rediscovery.
func collectResourceQueryFields(
	root *expr.AttributeExpr,
	att *expr.AttributeExpr,
	fields map[string]resourceQueryFieldDefinition,
	seen map[string]struct{},
) {
	if att == nil || att.Type == nil {
		return
	}
	hash := att.Type.Hash()
	if _, ok := seen[hash]; ok {
		return
	}
	seen[hash] = struct{}{}
	for _, base := range att.Bases {
		collectResourceQueryFields(root, attributeDataType(base), fields, seen)
	}
	for _, ref := range att.References {
		collectResourceQueryFields(root, attributeDataType(ref), fields, seen)
	}
	object := expr.AsObject(att.Type)
	if object == nil {
		return
	}
	for _, named := range *object {
		required := att.IsRequired(named.Name) || root.IsRequired(named.Name)
		fields[named.Name] = resourceQueryFieldDefinition{Attribute: named.Attribute, Required: required}
	}
}

// newResourceQueryField converts one flattened payload field into a concrete
// query-rendering plan for the client adapter template.
func newResourceQueryField(name string, definition resourceQueryFieldDefinition) (*ResourceQueryField, error) {
	if array := expr.AsArray(definition.Attribute.Type); array != nil {
		formatKind, err := resourceQueryFormatKind(name, array.ElemType.Type)
		if err != nil {
			return nil, err
		}
		valueType, parseBitSize := resourceQueryValueType(array.ElemType.Type)
		return &ResourceQueryField{
			QueryKey:     name,
			FormatKind:   formatKind,
			ValueType:    valueType,
			ParseBitSize: parseBitSize,
			Repeated:     true,
			Optional:     !definition.Required,
			attribute:    definition.Attribute,
		}, nil
	}

	formatKind, err := resourceQueryFormatKind(name, definition.Attribute.Type)
	if err != nil {
		return nil, err
	}
	valueType, parseBitSize := resourceQueryValueType(definition.Attribute.Type)
	return &ResourceQueryField{
		QueryKey:     name,
		FormatKind:   formatKind,
		ValueType:    valueType,
		ParseBitSize: parseBitSize,
		Optional:     !definition.Required,
		attribute:    definition.Attribute,
	}, nil
}

// bindResourceQuerySelectors copies each payload field name and pointer choice
// from Goa's service plan into the MCP resource adapter that emits the access.
func bindResourceQueryClientSelectors(plan *goaservice.Plan, resources []*mcpexpr.ResourceExpr, adapters []*ResourceAdapter) error {
	for index, resource := range resources {
		if resource.Method.Payload == nil || resource.Method.Payload.Type == expr.Empty {
			continue
		}
		layout, err := plan.MethodPayloadLayout(resource.Method)
		if err != nil {
			return fmt.Errorf("read payload layout for resource method %q: %w", resource.Method.Name, err)
		}
		if layout.Kind() != codegen.GoStruct {
			return fmt.Errorf("resource method %q payload must define Go fields", resource.Method.Name)
		}
		for _, queryField := range adapters[index].QueryFields {
			if err := bindResourceQuerySelector(layout, queryField); err != nil {
				return fmt.Errorf("bind resource method %q query field %q: %w", resource.Method.Name, queryField.QueryKey, err)
			}
		}
	}
	return nil
}

// bindResourceQuerySelector finds the top-level Goa field built from the query
// attribute and copies the exact selector used by generated service code.
func bindResourceQuerySelector(layout *codegen.GoTypePlan, queryField *ResourceQueryField) error {
	var selected *codegen.GoTypePlan
	for _, field := range layout.Fields() {
		if !field.MatchesOccurrence(queryField.attribute) {
			continue
		}
		if selected != nil {
			return fmt.Errorf("payload contains more than one field built from the same query attribute")
		}
		selected = field
	}
	if selected == nil {
		return fmt.Errorf("payload does not contain the query attribute")
	}
	queryField.ClientSelector = selected.FieldName(true)
	queryField.ClientPointer = selected.IsPointer()
	return nil
}

// resourceQueryValueType returns the Go primitive type and numeric range used
// when the generated server reads one query value.
func resourceQueryValueType(dt expr.DataType) (string, string) {
	underlying := resourceQueryUnderlyingType(dt)
	switch underlying.Kind() {
	case expr.StringKind:
		return "string", ""
	case expr.BooleanKind:
		return "bool", ""
	case expr.IntKind:
		return "int", "strconv.IntSize"
	case expr.Int32Kind:
		return "int32", "32"
	case expr.Int64Kind:
		return "int64", "64"
	case expr.UIntKind:
		return "uint", "strconv.IntSize"
	case expr.UInt32Kind:
		return "uint32", "32"
	case expr.UInt64Kind:
		return "uint64", "64"
	case expr.Float32Kind:
		return "float32", "32"
	case expr.Float64Kind:
		return "float64", "64"
	default:
		panic(fmt.Sprintf("unsupported resource query type %q", underlying.Name()))
	}
}

// attributeDataType recovers the full attribute metadata for base and reference
// types when they are modeled as named user types.
func attributeDataType(dt expr.DataType) *expr.AttributeExpr {
	if userType, ok := dt.(expr.UserType); ok {
		return userType.Attribute()
	}
	return &expr.AttributeExpr{Type: dt}
}

// resourceQueryFormatKind classifies one supported scalar query value so the
// template can emit direct string formatting without runtime JSON marshalling.
func resourceQueryFormatKind(fieldName string, dt expr.DataType) (string, error) {
	underlying := resourceQueryUnderlyingType(dt)
	if array := expr.AsArray(underlying); array != nil {
		return "", fmt.Errorf(
			`field %q uses nested array query values; expected primitive or array of primitive values`,
			fieldName,
		)
	}
	if !expr.IsPrimitive(underlying) {
		return "", fmt.Errorf(
			`field %q uses unsupported resource query type %q; expected primitive or array of primitive values`,
			fieldName,
			underlying.Name(),
		)
	}
	switch underlying.Kind() {
	case expr.StringKind:
		return resourceQueryFormatString, nil
	case expr.BooleanKind:
		return resourceQueryFormatBool, nil
	case expr.IntKind, expr.Int32Kind, expr.Int64Kind:
		return resourceQueryFormatInt, nil
	case expr.UIntKind, expr.UInt32Kind, expr.UInt64Kind:
		return resourceQueryFormatUint, nil
	case expr.Float32Kind:
		return resourceQueryFormatFloat32, nil
	case expr.Float64Kind:
		return resourceQueryFormatFloat64, nil
	case expr.BytesKind,
		expr.ArrayKind,
		expr.ObjectKind,
		expr.MapKind,
		expr.UnionKind,
		expr.UserTypeKind,
		expr.ResultTypeKind,
		expr.AnyKind:
		return "", fmt.Errorf(
			`field %q uses unsupported resource query type %q; expected string, bool, int, uint, float, or arrays of those values`,
			fieldName,
			underlying.Name(),
		)
	}
	return "", fmt.Errorf(
		`field %q uses unsupported resource query type %q; expected string, bool, int, uint, float, or arrays of those values`,
		fieldName,
		underlying.Name(),
	)
}

// resourceQueryUnderlyingType resolves aliases so query-field guard selection
// follows the concrete runtime kind that Goa will generate.
func resourceQueryUnderlyingType(dt expr.DataType) expr.DataType {
	switch actual := dt.(type) {
	case *expr.UserTypeExpr:
		return resourceQueryUnderlyingType(actual.Type)
	case *expr.ResultTypeExpr:
		return resourceQueryUnderlyingType(actual.Type)
	default:
		return actual
	}
}

// buildDynamicPromptAdapters creates adapter data for dynamic prompts
func (g *adapterGenerator) buildDynamicPromptAdapters() []*DynamicPromptAdapter {
	var adapters []*DynamicPromptAdapter

	for _, dp := range g.dynamicPrompts {
		hasRealPayload := dp.Method.Payload != nil && dp.Method.Payload.Type != expr.Empty

		adapter := &DynamicPromptAdapter{
			Name:           dp.Name,
			Description:    dp.Description,
			HasPayload:     hasRealPayload,
			userMethodName: dp.Method.Name,
		}

		if hasRealPayload {
			adapter.Arguments = g.promptArgsFromPayload(dp.Method.Payload)
			adapter.ExampleArguments = g.buildExampleJSON(dp.Method)
		} else {
			adapter.ExampleArguments = "{}"
		}

		if hasMCPValue(dp.Method.Result) {
			adapter.ResultType = g.getResultTypeReference(dp.Method.Result)
		}

		adapters = append(adapters, adapter)
	}

	return adapters
}

// hasMCPValue reports whether a method side carries application data.
func hasMCPValue(attribute *expr.AttributeExpr) bool {
	return attribute != nil && attribute.Type != nil && attribute.Type != expr.Empty
}

// buildExampleJSON returns a repeatable example for method's payload.
func (g *adapterGenerator) buildExampleJSON(method *expr.MethodExpr) string {
	attr := method.Payload
	if attr == nil || attr.Type == nil || attr.Type == expr.Empty {
		return "{}"
	}
	r := expr.NewExampleGenerator(expr.NewDeterministicRandomizerFactory()).At(
		expr.MethodPayloadExampleIdentity(method),
	)
	v := attr.Example(r)
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// promptArgsFromPayload builds a flat list of prompt arguments from a payload attribute (top-level only)
func (g *adapterGenerator) promptArgsFromPayload(attr *expr.AttributeExpr) []PromptArg {
	if attr == nil || attr.Type == nil || attr.Type == expr.Empty {
		return nil
	}
	// Unwrap user type
	if ut, ok := attr.Type.(expr.UserType); ok {
		return g.promptArgsFromPayload(ut.Attribute())
	}
	obj, ok := attr.Type.(*expr.Object)
	if !ok {
		return nil
	}
	// Pre-allocate based on number of top-level fields
	out := make([]PromptArg, 0, len(*obj))
	// Build required set
	required := map[string]struct{}{}
	if attr.Validation != nil {
		for _, n := range attr.Validation.Required {
			required[n] = struct{}{}
		}
	}
	for _, nat := range *obj {
		name := nat.Name
		desc := ""
		if nat.Attribute != nil && nat.Attribute.Description != "" {
			desc = nat.Attribute.Description
		}
		_, req := required[name]
		out = append(out, PromptArg{Name: name, Description: desc, Required: req})
	}
	return out
}

// buildNotificationAdapters creates adapter data for notifications
func (g *adapterGenerator) buildNotificationAdapters() []*NotificationAdapter {
	adapters := make([]*NotificationAdapter, 0)
	if g.mcp != nil {
		for _, n := range g.mcp.Notifications {
			wireMethodName := "notify_" + n.Name
			adapters = append(adapters, &NotificationAdapter{
				Name:           n.Name,
				Description:    n.Description,
				WireMethodName: wireMethodName,
				userMethodName: n.Method.Name,
			})
		}
	}
	return adapters
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

// getResultTypeReference returns the result type shown in generated tool
// registration metadata.
func (g *adapterGenerator) getResultTypeReference(attr *expr.AttributeExpr) string {
	// Service package alias used in adapter imports.
	svcAlias := codegen.SnakeCase(g.originalService.Name)
	// External user types should be qualified with their locator package alias.
	if ut, ok := attr.Type.(expr.UserType); ok && ut != nil {
		if loc := codegen.UserTypeLocation(ut); loc != nil && loc.PackageName() != "" {
			return g.scope.GoFullTypeRef(attr, loc.PackageName())
		}
	}
	// For composites and service-local user types, qualify nested refs with service alias.
	return g.scope.GoFullTypeRef(attr, svcAlias)
}
