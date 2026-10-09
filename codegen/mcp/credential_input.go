// Package codegen binds Goa's annotated credentials to typed HTTP input fields.
// Protocol parameters keep only MCP data. The adapter fills the original
// service payload from its native header, query or cookie before calling the
// configured endpoint; credentials never become model-visible arguments.
package codegen

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// credentialInput retains one service credential and its native HTTP input.
	credentialInput struct {
		// Name identifies the original payload field.
		Name string
		// Required records whether the authored payload requires its presence.
		Required bool
		// Bearer removes the required Bearer scheme from Authorization input.
		Bearer bool
		// Basic selects the standard Basic header decoder.
		Basic bool
		// AlternativeScheme leaves this credential absent when the shared header
		// carries the other authored authentication alternative.
		AlternativeScheme string
		// Username selects the username rather than password from Basic input.
		Username bool
		// Target names the original service field.
		Target string
		// TypeRef names its authored string type without a presence pointer.
		TypeRef string
		// Pointer records the original service field's presence pointer.
		Pointer bool
		// Sources maps each protocol method to its generated HTTP input selector.
		Sources map[string]string

		sourceNames   map[string]string
		location      string
		transportName string
	}

	// protocolHTTPInputs keeps native URL and credential fields outside MCP parameters.
	protocolHTTPInputs struct {
		body   *expr.AttributeExpr
		fields []*protocolHTTPField
	}

	// protocolHTTPField describes one native input shared within a protocol method.
	protocolHTTPField struct {
		name          string
		attribute     *expr.AttributeExpr
		location      string
		transportName string
	}
)

const (
	credentialAuthorizationHeader = "Authorization"
	credentialHeaderLocation      = "header"
	credentialQueryLocation       = "query"
)

// prepareHTTPInputs adds URL values to every protocol method and credentials
// to methods that dispatch secured operations. MCP parameter bodies stay
// unchanged; selected service calls receive these native inputs separately.
func prepareHTTPInputs(root *expr.RootExpr, service *expr.ServiceExpr, mcp *mcpexpr.MCPExpr, protocol *expr.ServiceExpr, paths *expr.MappedAttributeExpr) (map[string][]*credentialInput, map[string]*protocolHTTPInputs, error) {
	operations := make(map[string][]*expr.MethodExpr)
	for _, tool := range mcp.Tools {
		operations["tools/call"] = append(operations["tools/call"], tool.Method)
		binding, err := mcpinput.TaskExchange(tool.Method)
		if err != nil {
			return nil, nil, err
		}
		if binding != nil {
			operations["tasks/get"] = append(operations["tasks/get"], binding.Read)
			operations["tasks/update"] = append(operations["tasks/update"], binding.Answer)
			operations["tasks/cancel"] = append(operations["tasks/cancel"], binding.Cancel)
		}
	}
	for _, resource := range mcp.Resources {
		operations["resources/read"] = append(operations["resources/read"], resource.Method)
	}
	if mcp.ResourceReader != nil {
		operations["resources/read"] = append(operations["resources/read"], mcp.ResourceReader)
	}
	for _, prompt := range mcp.MethodPrompts {
		operations["prompts/get"] = append(operations["prompts/get"], prompt.Method)
	}
	for _, completion := range mcp.PromptCompletions {
		operations["completion/complete"] = append(operations["completion/complete"], completion.Method)
	}
	for _, completion := range mcp.ResourceCompletions {
		operations["completion/complete"] = append(operations["completion/complete"], completion.Method)
	}
	if mcp.ToolCatalog != nil {
		operations["tools/list"] = []*expr.MethodExpr{mcp.ToolCatalog}
	}
	if mcp.PromptCatalog != nil {
		operations["prompts/list"] = []*expr.MethodExpr{mcp.PromptCatalog}
	}
	if mcp.ResourceCatalog != nil {
		operations["resources/list"] = []*expr.MethodExpr{mcp.ResourceCatalog}
	}
	if mcp.ResourceTemplateCatalog != nil {
		operations["resources/templates/list"] = []*expr.MethodExpr{mcp.ResourceTemplateCatalog}
	}
	if mcp.SkillCatalog != nil {
		operations["skills/list"] = []*expr.MethodExpr{mcp.SkillCatalog}
	}
	if mcp.SkillLookup != nil {
		operations["skills/get"] = []*expr.MethodExpr{mcp.SkillLookup}
	}
	if mcp.ResourceDirectory != nil {
		operations["resources/directory/read"] = []*expr.MethodExpr{mcp.ResourceDirectory}
	}
	if source := mcp.SubscriptionSource; source != nil {
		operations["subscriptions/listen"] = []*expr.MethodExpr{source.Method}
		if selected := source.Method.Payload.Find("tasks"); selected != nil {
			for _, field := range *expr.AsObject(selected.Type) {
				binding, err := mcpinput.TaskExchange(service.Method(field.Name))
				if err != nil {
					return nil, nil, err
				}
				operations["subscriptions/listen"] = append(operations["subscriptions/listen"], binding.Read)
			}
		}
	}
	credentials := make(map[string][]*credentialInput)
	bodies := make(map[string]*protocolHTTPInputs)
	for _, operation := range protocol.Methods {
		methods := slices.Clone(operations[operation.Name])
		slices.SortFunc(methods, func(left, right *expr.MethodExpr) int {
			if left.Name < right.Name {
				return -1
			}
			if left.Name > right.Name {
				return 1
			}
			return 0
		})
		methods = slices.Compact(methods)
		inputs := &protocolHTTPInputs{body: operation.Payload}
		for _, path := range *expr.AsObject(paths.Type) {
			inputs.fields = append(inputs.fields, &protocolHTTPField{
				name:          path.Name,
				attribute:     expr.DupAtt(path.Attribute),
				location:      "path",
				transportName: paths.ElemName(path.Name),
			})
		}
		fields := make(map[[2]string]*protocolHTTPField)
		for _, method := range methods {
			selected, known := credentials[method.Name]
			if !known {
				var err error
				selected, err = nativeCredentialInputs(root, service, method)
				if err != nil {
					return nil, nil, err
				}
				credentials[method.Name] = selected
			}
			for _, credential := range selected {
				key := [2]string{credential.location, credential.transportName}
				source := fields[key]
				if source == nil {
					source = &protocolHTTPField{
						name:          fmt.Sprintf("httpCredential%d", len(inputs.fields)),
						attribute:     &expr.AttributeExpr{Type: expr.String, Description: "Native HTTP authentication input; excluded from MCP parameters."},
						location:      credential.location,
						transportName: credential.transportName,
					}
					fields[key] = source
					inputs.fields = append(inputs.fields, source)
				}
				credential.sourceNames[operation.Name] = source.name
			}
		}
		if len(inputs.fields) == 0 {
			continue
		}
		// An inline payload lets Goa name each service's complete protocol input.
		// The shared MCP body declaration remains free of URL values and credentials.
		operation.Payload = expr.DupAtt(operation.Payload.Type.(expr.UserType).Attribute())
		object := expr.AsObject(operation.Payload.Type)
		for _, field := range inputs.fields {
			object.Set(field.name, field.attribute)
		}
		bodies[operation.Name] = inputs
	}
	return credentials, bodies, nil
}

// nativeCredentialInputs reads the evaluated native transport requirements.
// Body credentials cannot retain their binding in MCP, whose parameters are
// domain input. Such a declaration fails instead of inventing another binding.
func nativeCredentialInputs(root *expr.RootExpr, service *expr.ServiceExpr, method *expr.MethodExpr) ([]*credentialInput, error) {
	names := mcpinput.Credentials(method.Payload)
	if len(names) == 0 {
		return nil, nil
	}
	requirements := nativeCredentialRequirements(root, service, method)
	byName := make(map[string]*credentialInput)
	for _, requirement := range requirements {
		var combined []*credentialInput
		for _, scheme := range requirement.Schemes {
			data := goaservice.BuildSchemeData(scheme, method)
			if data == nil {
				continue
			}
			fields := []string{data.KeyAttr}
			if scheme.Kind == expr.BasicAuthKind {
				fields = []string{data.UsernameAttr, data.PasswordAttr}
			}
			for _, name := range fields {
				input := &credentialInput{
					Name: name, Required: method.Payload.IsRequired(name),
					Sources: make(map[string]string), sourceNames: make(map[string]string),
					location: scheme.In, transportName: scheme.Name,
				}
				if scheme.Kind == expr.BasicAuthKind {
					input.Basic, input.Username = true, name == data.UsernameAttr
					input.location, input.transportName = credentialHeaderLocation, credentialAuthorizationHeader
				} else if input.transportName == "" {
					input.location, input.transportName = credentialHeaderLocation, credentialAuthorizationHeader
				}
				if input.location == credentialHeaderLocation {
					input.transportName = http.CanonicalHeaderKey(input.transportName)
					switch input.transportName {
					case "Content-Type", "Accept", "Mcp-Protocol-Version", "Mcp-Method", "Mcp-Name":
						return nil, fmt.Errorf("MCP method %q credential %q uses protocol-owned header %q", method.Name, name, input.transportName)
					}
					if strings.HasPrefix(input.transportName, "Mcp-Param-") {
						return nil, fmt.Errorf("MCP method %q credential %q uses protocol-owned header %q", method.Name, name, input.transportName)
					}
				}
				input.Bearer = !input.Basic && input.location == credentialHeaderLocation && input.transportName == credentialAuthorizationHeader && scheme.Kind != expr.APIKeyKind
				if scheme.Kind == expr.OAuth2Kind && !input.Bearer {
					return nil, fmt.Errorf("MCP method %q OAuth credential %q must use Authorization: Bearer; native %s binding %q cannot carry an MCP access token", method.Name, name, input.location, input.transportName)
				}
				switch input.location {
				case credentialHeaderLocation, credentialQueryLocation, "cookie":
				default:
					return nil, fmt.Errorf("MCP method %q credential %q requires a header, query or cookie binding; native %q binding cannot enter MCP arguments", method.Name, name, input.location)
				}
				if previous := byName[name]; previous != nil {
					if previous.location != input.location || previous.transportName != input.transportName || previous.Basic != input.Basic || previous.Bearer != input.Bearer || previous.Username != input.Username {
						return nil, fmt.Errorf("MCP method %q credential %q has conflicting native authentication bindings", method.Name, name)
					}
					input = previous
				} else {
					byName[name] = input
				}
				combined = append(combined, input)
			}
		}
		// One requirement combines all its schemes. A Basic and Bearer value
		// cannot both occupy its single Authorization header.
		for _, basic := range combined {
			if !basic.Basic {
				continue
			}
			for _, bearer := range combined {
				if bearer.Bearer {
					return nil, fmt.Errorf("MCP method %q combines Basic and Bearer authentication in one Authorization header", method.Name)
				}
			}
		}
	}
	inputs := make([]*credentialInput, 0, len(names))
	for _, name := range names {
		input := byName[name]
		if input == nil {
			return nil, fmt.Errorf("MCP method %q credential %q has no native authentication binding", method.Name, name)
		}
		inputs = append(inputs, input)
	}
	// Separate requirements are alternatives. Their inactive credential fields
	// must permit absence so the original payload can retain its validation.
	for _, basic := range inputs {
		if !basic.Basic {
			continue
		}
		for _, bearer := range inputs {
			if !bearer.Bearer {
				continue
			}
			if basic.Required || bearer.Required {
				return nil, fmt.Errorf("MCP method %q Basic and Bearer alternatives require optional credential fields; one Authorization header cannot supply the inactive alternative", method.Name)
			}
			basic.AlternativeScheme, bearer.AlternativeScheme = "Bearer", "Basic"
		}
	}
	return inputs, nil
}

// nativeCredentialRequirements selects the authored JSON-RPC binding when
// present, otherwise the authored HTTP binding. Methods without either use
// Goa's implicit Authorization header; no transport binding is invented.
func nativeCredentialRequirements(root *expr.RootExpr, service *expr.ServiceExpr, method *expr.MethodExpr) []*expr.SecurityExpr {
	for _, transports := range [][]*expr.HTTPServiceExpr{root.API.JSONRPC.Services, root.API.HTTP.Services} {
		for _, transport := range transports {
			if transport.ServiceExpr != service {
				continue
			}
			for _, endpoint := range transport.HTTPEndpoints {
				if endpoint.MethodExpr == method {
					return expr.EffectiveSecurityRequirements(endpoint.Requirements)
				}
			}
		}
	}
	return expr.EffectiveSecurityRequirements(method.Requirements)
}

// bindProtocolHTTPInputs gives Goa the parameter-only body and native
// mappings. Goa generates URL, header, query, and cookie decoding and encoding.
func bindProtocolHTTPInputs(endpoint *expr.HTTPEndpointExpr, inputs *protocolHTTPInputs) {
	endpoint.Body = inputs.body
	for _, field := range inputs.fields {
		var mapped *expr.MappedAttributeExpr
		switch field.location {
		case credentialHeaderLocation:
			mapped = endpoint.Headers
		case "path", credentialQueryLocation:
			mapped = endpoint.Params
		case "cookie":
			mapped = endpoint.Cookies
		}
		expr.AsObject(mapped.Type).Set(field.name, field.attribute)
		mapped.Map(field.transportName, field.name)
		if field.location == "path" {
			endpoint.MethodExpr.Payload.Validation.AddRequired(field.name)
		}
	}
}

// bindCredentialSelectors resolves original and protocol fields through Goa's
// saved layouts. Named strings and optional pointers retain their authored types.
func bindCredentialSelectors(prepared *preparedMCPService, data *AdapterData) error {
	for _, endpoint := range data.EndpointMethods {
		if len(endpoint.Credentials) == 0 {
			continue
		}
		for _, credential := range endpoint.Credentials {
			targets := endpoint.payloadLayout.PlansForOccurrence(endpoint.method.Payload.Find(credential.Name))
			if len(targets) != 1 || targets[0].FieldName(true) == "" {
				return fmt.Errorf("MCP method %q credential %q must resolve to one original payload field (found %d)", endpoint.method.Name, credential.Name, len(targets))
			}
			field := targets[0]
			credential.Target = field.FieldName(true)
			credential.TypeRef = field.Link(data.mcpImportPath, data.mcpPackage.ImportName).RefWithPointer(false)
			credential.Pointer = field.IsPointer()
			for operation, name := range credential.sourceNames {
				method := prepared.mcpService.Method(operation)
				layout := prepared.protocolLayouts[operation]
				sources := layout.PlansForOccurrence(method.Payload.Find(name))
				if len(sources) != 1 || sources[0].FieldName(true) == "" {
					return fmt.Errorf("MCP operation %q credential input must resolve to one protocol payload field", operation)
				}
				credential.Sources[operation] = sources[0].FieldName(true)
			}
		}
	}
	return nil
}

// credentialInputImports reserves only the parsers used by authored profiles.
func credentialInputImports(credentials map[string][]*credentialInput) []*codegen.ImportSpec {
	var basic, bearer bool
	for _, fields := range credentials {
		for _, field := range fields {
			basic = basic || field.Basic
			bearer = bearer || field.Bearer
		}
	}
	var imports []*codegen.ImportSpec
	if basic {
		imports = append(imports, codegen.SimpleImport("net/http"))
	}
	if bearer {
		imports = append(imports, codegen.SimpleImport("strings"))
	}
	return imports
}
