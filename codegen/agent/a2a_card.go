package codegen

import (
	"path/filepath"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	goaexpr "goa.design/goa/v3/expr"
)

// a2aPkgName is the package name for A2A generated files.
const a2aPkgName = "a2a"

type (
	// A2ACardData holds the template-ready data for generating an A2A agent card.
	A2ACardData struct {
		// Agent contains the agent metadata.
		Agent *AgentData
		// Skills contains the skills derived from exported tools.
		Skills []*A2ASkillData
		// Security contains the security scheme data.
		Security *A2ASecurityData
	}

	// A2ASkillData holds the template-ready data for an A2A skill.
	A2ASkillData struct {
		// ID is the skill identifier (matches the tool's qualified name).
		ID string
		// Name is the human-readable skill name.
		Name string
		// Description explains what the skill does.
		Description string
		// Tags are metadata tags for discovery and filtering.
		Tags []string
		// PayloadTypeRef is the Go type reference for the payload (computed via GoFullTypeRef).
		PayloadTypeRef string
		// ResultTypeRef is the Go type reference for the result (computed via GoFullTypeRef).
		ResultTypeRef string
		// InputSchema is the JSON Schema for the skill input.
		InputSchema string
		// ExampleArgs contains a minimal valid JSON for skill arguments.
		ExampleArgs string
	}

	// A2ASecuritySchemeData holds the template-ready data for an A2A security scheme.
	A2ASecuritySchemeData struct {
		// Name is the security scheme name.
		Name string
		// Type is the A2A security scheme type (e.g., "http", "apiKey", "oauth2").
		Type string
		// Scheme is the HTTP authentication scheme (e.g., "bearer", "basic").
		Scheme string
		// In specifies where the API key is sent (e.g., "header", "query").
		In string
		// ParamName is the parameter name for API key authentication.
		ParamName string
		// Flows contains OAuth2 flow configurations.
		Flows *A2AOAuth2FlowsData
	}

	// A2AOAuth2FlowsData holds OAuth2 flow configurations.
	A2AOAuth2FlowsData struct {
		// ClientCredentials is the client credentials flow configuration.
		ClientCredentials *A2AOAuth2FlowData
		// AuthorizationCode is the authorization code flow configuration.
		AuthorizationCode *A2AOAuth2FlowData
	}

	// A2AOAuth2FlowData represents a single OAuth2 flow configuration.
	A2AOAuth2FlowData struct {
		// TokenURL is the token endpoint URL.
		TokenURL string
		// AuthorizationURL is the authorization endpoint URL.
		AuthorizationURL string
		// Scopes maps scope names to descriptions.
		Scopes map[string]string
	}
)

// a2aCardFiles generates the A2A agent card files for agents that have
// exported toolsets with PublishTo configuration.
func a2aCardFiles(agent *AgentData, security *A2ASecurityData) []*codegen.File {
	// Only generate A2A card if the agent has exported toolsets
	if len(agent.ExportedToolsets) == 0 {
		return nil
	}

	// Check if any exported toolset has PublishTo configured
	hasPublishTo := false
	for _, ts := range agent.ExportedToolsets {
		if ts.Expr != nil && len(ts.Expr.PublishTo) > 0 {
			hasPublishTo = true
			break
		}
	}

	// Generate A2A card even without PublishTo for A2A compatibility
	// (agents with exports should be discoverable)
	_ = hasPublishTo

	data := buildA2ACardData(agent, security)
	if data == nil {
		return nil
	}

	var files []*codegen.File

	// Generate types.go (shared A2A types)
	typesFile := a2aTypesFile(agent)
	if typesFile != nil {
		files = append(files, typesFile)
	}

	// Generate card.go
	cardFile := a2aCardFile(agent, data)
	if cardFile != nil {
		files = append(files, cardFile)
	}

	return files
}

// a2aTypesFile generates the types.go file with shared A2A types.
// This establishes a single source of truth for A2A types used by
// both the card and client packages.
func a2aTypesFile(agent *AgentData) *codegen.File {
	// Output path: gen/<service>/agents/<agent>/a2a/types.go
	dir := filepath.Join(agent.Dir, a2aPkgName)

	imports := []*codegen.ImportSpec{
		{Path: "fmt"},
	}

	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.GoName+" A2A types", a2aPkgName, imports),
		{
			Name:   "a2a-types",
			Source: agentsTemplates.Read(a2aTypesFileT),
		},
	}

	return &codegen.File{
		Path:             filepath.Join(dir, "types.go"),
		SectionTemplates: sections,
	}
}

// a2aCardFile generates the card.go file for an agent's A2A card.
func a2aCardFile(agent *AgentData, data *A2ACardData) *codegen.File {
	// Output path: gen/<service>/agents/<agent>/a2a/card.go
	dir := filepath.Join(agent.Dir, a2aPkgName)

	imports := []*codegen.ImportSpec{}

	sections := []*codegen.SectionTemplate{
		codegen.Header(agent.GoName+" A2A agent card", a2aPkgName, imports),
		{
			Name:    "a2a-card",
			Source:  agentsTemplates.Read(a2aCardFileT),
			Data:    data,
			FuncMap: templateFuncMap(),
		},
	}

	return &codegen.File{
		Path:             filepath.Join(dir, "card.go"),
		SectionTemplates: sections,
	}
}

// buildA2ACardData transforms agent data into A2A card template data.
func buildA2ACardData(agent *AgentData, security *A2ASecurityData) *A2ACardData {
	data := &A2ACardData{
		Agent:    agent,
		Skills:   make([]*A2ASkillData, 0),
		Security: security,
	}

	// Build skills from exported tools
	for _, ts := range agent.ExportedToolsets {
		for _, tool := range ts.Tools {
			skill := &A2ASkillData{
				ID:          tool.QualifiedName,
				Name:        tool.Title,
				Description: tool.Description,
				Tags:        tool.Tags,
			}
			// Use tool name as fallback for title
			if skill.Name == "" {
				skill.Name = tool.Name
			}
			data.Skills = append(data.Skills, skill)
		}
	}

	return data
}

// HasSecuritySchemes returns true if security schemes are present.
func (d *A2ACardData) HasSecuritySchemes() bool {
	return d.Security != nil && d.Security.HasSecuritySchemes
}

// SecuritySchemes returns the security scheme definitions.
func (d *A2ACardData) SecuritySchemes() []*A2ASecuritySchemeData {
	if d.Security == nil {
		return nil
	}
	return d.Security.SecuritySchemes
}

// SecurityRequirements returns the security requirements.
func (d *A2ACardData) SecurityRequirements() []map[string][]string {
	if d.Security == nil {
		return nil
	}
	return d.Security.SecurityRequirements
}

// buildA2ASecurityData extracts security schemes from the agent's service.
// Returns scheme definitions and security requirements in A2A format.
// Since service.Data.Schemes is a flat list aggregated from all methods,
// we create one requirement per scheme (OR relationship - any scheme works).
func buildA2ASecurityData(agent *AgentData) ([]*A2ASecuritySchemeData, []map[string][]string) {
	if len(agent.Service.Schemes) == 0 {
		return nil, nil
	}

	schemes := make([]*A2ASecuritySchemeData, 0, len(agent.Service.Schemes))
	requirements := make([]map[string][]string, 0, len(agent.Service.Schemes))
	seenSchemes := make(map[string]bool)

	for _, scheme := range agent.Service.Schemes {
		if seenSchemes[scheme.SchemeName] {
			continue
		}
		seenSchemes[scheme.SchemeName] = true

		schemeData := mapServiceSchemeToA2A(scheme)
		if schemeData != nil {
			schemes = append(schemes, schemeData)
			// Each scheme is an alternative (OR) - create separate requirement
			requirements = append(requirements, map[string][]string{
				scheme.SchemeName: scheme.Scopes,
			})
		}
	}

	return schemes, requirements
}

// mapServiceSchemeToA2A converts a Goa service.SchemeData to A2A format.
func mapServiceSchemeToA2A(scheme *service.SchemeData) *A2ASecuritySchemeData {
	data := &A2ASecuritySchemeData{
		Name: scheme.SchemeName,
	}

	switch scheme.Type {
	case "Basic":
		data.Type = "http"
		data.Scheme = "basic"

	case "APIKey":
		data.Type = "apiKey"
		data.In = scheme.In
		data.ParamName = scheme.Name

	case "JWT":
		data.Type = "http"
		data.Scheme = "bearer"

	case "OAuth2":
		data.Type = "oauth2"
		data.Flows = buildA2AOAuth2FlowsFromService(scheme)

	default:
		return nil
	}

	return data
}

// buildA2AOAuth2FlowsFromService constructs OAuth2 flow configurations from a service scheme.
func buildA2AOAuth2FlowsFromService(scheme *service.SchemeData) *A2AOAuth2FlowsData {
	if len(scheme.Flows) == 0 {
		return nil
	}

	flows := &A2AOAuth2FlowsData{}

	for _, flow := range scheme.Flows {
		flowData := &A2AOAuth2FlowData{
			TokenURL:         flow.TokenURL,
			AuthorizationURL: flow.AuthorizationURL,
			Scopes:           make(map[string]string),
		}

		// Copy scopes from the scheme
		for _, scope := range scheme.Scopes {
			flowData.Scopes[scope] = scope
		}

		switch flow.Kind {
		case goaexpr.ClientCredentialsFlowKind:
			flows.ClientCredentials = flowData
		case goaexpr.AuthorizationCodeFlowKind:
			flows.AuthorizationCode = flowData
		case goaexpr.ImplicitFlowKind, goaexpr.PasswordFlowKind:
			// Implicit and password flows are not commonly used in A2A;
			// skip them for now.
		}
	}

	return flows
}
