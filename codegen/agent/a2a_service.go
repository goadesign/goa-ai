package codegen

import (
	"fmt"
	"path/filepath"
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
	httpcodegen "goa.design/goa/v3/http/codegen"
	jsonrpccodegen "goa.design/goa/v3/jsonrpc/codegen"
)

// a2aPackageName returns the Go package name for an A2A service.
// It uses Goify to match Goa's package naming convention (removes underscores).
func a2aPackageName(agentName string) string {
	return strings.ToLower(codegen.Goify("a2a_"+agentName, false))
}

// A2ASecurityData holds security scheme information for A2A generation.
// This is computed once and passed to all A2A generators.
type A2ASecurityData struct {
	// HasSecuritySchemes indicates whether security schemes are present.
	HasSecuritySchemes bool
	// SecuritySchemes contains the A2A security scheme definitions.
	SecuritySchemes []*A2ASecuritySchemeData
	// SecurityRequirements contains the security requirements.
	SecurityRequirements []map[string][]string
}

// A2AAdapterData holds the data for generating the A2A adapter.
type A2AAdapterData struct {
	// Agent is the agent data.
	Agent *AgentData
	// A2AServiceName is the name of the generated A2A service (e.g., "a2a_myagent").
	A2AServiceName string
	// A2APackage is the package name for the A2A service.
	A2APackage string
	// Skills are the A2A skills derived from exported tools.
	Skills []*A2ASkillData
	// Security contains the security scheme data.
	Security *A2ASecurityData
	// ProtocolVersion is the A2A protocol version.
	ProtocolVersion string
}

// HasSecuritySchemes returns true if security schemes are present.
func (d *A2AAdapterData) HasSecuritySchemes() bool {
	return d.Security != nil && d.Security.HasSecuritySchemes
}

// SecuritySchemes returns the security scheme definitions.
func (d *A2AAdapterData) SecuritySchemes() []*A2ASecuritySchemeData {
	if d.Security == nil {
		return nil
	}
	return d.Security.SecuritySchemes
}

// SecurityRequirements returns the security requirements.
func (d *A2AAdapterData) SecurityRequirements() []map[string][]string {
	if d.Security == nil {
		return nil
	}
	return d.Security.SecurityRequirements
}

// a2aServiceFiles generates the A2A service files for agents that have
// exported toolsets. This follows the same pattern as MCP service generation.
func a2aServiceFiles(genpkg string, agent *AgentData, security *A2ASecurityData) []*codegen.File {
	// Only generate A2A service if the agent has exported toolsets
	if len(agent.ExportedToolsets) == 0 {
		return nil
	}

	var files []*codegen.File

	// Build A2A expression
	exprBuilder := newA2AExprBuilder(agent)
	a2aService := exprBuilder.BuildServiceExpr()

	// Create temporary root for A2A generation
	a2aRoot := exprBuilder.BuildRootExpr(a2aService)

	// Prepare, validate, and finalize A2A expressions
	if err := exprBuilder.PrepareAndValidate(a2aRoot); err != nil {
		// Log error but don't fail generation
		return nil
	}

	// Build adapter data with security using the adapter generator
	// This computes type references, schemas, and example args
	adapterGen := newA2AAdapterGenerator(genpkg, agent)
	adapterData := adapterGen.BuildAdapterData(security)

	// Generate A2A service code using Goa's standard generators
	serviceFiles := generateA2AServiceCode(genpkg, a2aRoot, a2aService, agent)
	files = append(files, serviceFiles...)

	// Generate A2A adapter that maps protocol to agent runtime
	adapterFiles := generateA2AAdapter(genpkg, agent, adapterData)
	files = append(files, adapterFiles...)

	// Generate static configuration (ServerConfig/ProviderConfig) for this agent.
	if regFile := a2aRegisterFile(adapterData); regFile != nil {
		files = append(files, regFile)
	}

	// Generate server stub that constructs runtime/a2a.Server from the config.
	if stubFile := a2aServerStubFile(adapterData); stubFile != nil {
		files = append(files, stubFile)
	}

	return files
}

// buildA2ASecurityDataFromAgent extracts security schemes from the agent's service
// and returns them in A2A format. This is called once and passed to all generators.
func buildA2ASecurityDataFromAgent(agent *AgentData) *A2ASecurityData {
	schemes, requirements := buildA2ASecurityData(agent)
	return &A2ASecurityData{
		HasSecuritySchemes:   len(schemes) > 0,
		SecuritySchemes:      schemes,
		SecurityRequirements: requirements,
	}
}

// buildA2AAdapterData creates the adapter data for template rendering.
func buildA2AAdapterData(agent *AgentData, security *A2ASecurityData) *A2AAdapterData {
	data := &A2AAdapterData{
		Agent:           agent,
		A2AServiceName:  "a2a_" + agent.Name,
		A2APackage:      a2aPackageName(agent.Name),
		ProtocolVersion: "1.0",
		Skills:          make([]*A2ASkillData, 0),
		Security:        security,
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
			if skill.Name == "" {
				skill.Name = tool.Name
			}
			data.Skills = append(data.Skills, skill)
		}
	}

	return data
}

// generateA2AServiceCode generates the A2A service layer and JSON-RPC transport
// using Goa's built-in generators.
func generateA2AServiceCode(genpkg string, root *expr.RootExpr, a2aService *expr.ServiceExpr, _ *AgentData) []*codegen.File {
	files := make([]*codegen.File, 0, 16)

	// Create services data from temporary A2A root
	servicesData := service.NewServicesData(root)

	// Generate A2A service layer
	userTypePkgs := make(map[string][]string)
	serviceFiles := service.Files(genpkg, a2aService, servicesData, userTypePkgs)
	files = append(files, serviceFiles...)
	files = append(files, service.EndpointFile(genpkg, a2aService, servicesData))
	files = append(files, service.ClientFile(genpkg, a2aService, servicesData))

	// Generate JSON-RPC transport for A2A service
	httpServices := httpcodegen.NewServicesData(servicesData, &root.API.JSONRPC.HTTPExpr)
	httpServices.Root = root

	// Generate server files
	base := jsonrpccodegen.ServerFiles(genpkg, httpServices)
	sse := jsonrpccodegen.SSEServerFiles(genpkg, httpServices)
	files = append(files, base...)
	files = append(files, sse...)
	files = append(files, jsonrpccodegen.ServerTypeFiles(genpkg, httpServices)...)
	files = append(files, jsonrpccodegen.PathFiles(httpServices)...)

	// Generate client files
	files = append(files, jsonrpccodegen.ClientTypeFiles(genpkg, httpServices)...)
	clientFiles := jsonrpccodegen.ClientFiles(genpkg, httpServices)
	// Patch client files to add SSE Accept headers for streaming methods
	patchA2AJSONRPCClientFiles(genpkg, a2aService, clientFiles)
	files = append(files, clientFiles...)

	return files
}

// generateA2AAdapter generates the adapter that maps A2A protocol to agent runtime.
func generateA2AAdapter(_ string, agent *AgentData, data *A2AAdapterData) []*codegen.File {
	var files []*codegen.File

	agentName := codegen.SnakeCase(agent.Name)
	pkgName := a2aPackageName(agent.Name)

	// Generate adapter_server.go with modular template sections
	adapterPath := filepath.Join(codegen.Gendir, "a2a_"+agentName, "adapter_server.go")
	adapterImports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "encoding/json"},
		{Path: "fmt"},
		{Path: "sync"},
		{Path: "time"},
		// Note: No import for the A2A service package since the adapter is in the same package
		{Path: "goa.design/goa-ai/runtime/agent", Name: "agentruntime"},
	}

	// Common FuncMap for all template sections
	funcMap := map[string]any{
		"goify":   func(s string) string { return codegen.Goify(s, true) },
		"comment": codegen.Comment,
		"quote":   func(s string) string { return fmt.Sprintf("%q", s) },
	}

	files = append(files, &codegen.File{
		Path: adapterPath,
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header(fmt.Sprintf("A2A server adapter for %s agent", agent.Name), pkgName, adapterImports),
			{
				Name:    "a2a-adapter-core",
				Source:  agentsTemplates.Read(a2aAdapterCoreFileT),
				Data:    data,
				FuncMap: funcMap,
			},
			{
				Name:    "a2a-adapter-tasks",
				Source:  agentsTemplates.Read(a2aAdapterTasksFileT),
				Data:    data,
				FuncMap: funcMap,
			},
			{
				Name:    "a2a-adapter-card",
				Source:  agentsTemplates.Read(a2aAdapterCardFileT),
				Data:    data,
				FuncMap: funcMap,
			},
		},
	})

	// Generate protocol_version.go
	versionPath := filepath.Join(codegen.Gendir, "a2a_"+agentName, "protocol_version.go")
	files = append(files, &codegen.File{
		Path: versionPath,
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header("A2A protocol version", pkgName, nil),
			{
				Name:   "a2a-protocol-version",
				Source: fmt.Sprintf("const DefaultProtocolVersion = %q\n", data.ProtocolVersion),
			},
		},
	})

	return files
}

// patchA2AJSONRPCClientFiles mutates generated JSON-RPC client files to add
// SSE Accept headers for streaming methods, context cancellation support,
// and structured error types.
func patchA2AJSONRPCClientFiles(_ string, a2aService *expr.ServiceExpr, clientFiles []*codegen.File) {
	// a2aService.Name is already prefixed with "a2a_"; derive original service snake name
	svcNameAll := codegen.SnakeCase(a2aService.Name) // e.g., "a2a_assistant"

	clientPath := filepath.Join(codegen.Gendir, "jsonrpc", svcNameAll, "client", "client.go")
	encodeDecodePath := filepath.Join(codegen.Gendir, "jsonrpc", svcNameAll, "client", "encode_decode.go")
	streamPath := filepath.Join(codegen.Gendir, "jsonrpc", svcNameAll, "client", "stream.go")

	// Helper to add imports to a file
	addImports := func(f *codegen.File, specs ...*codegen.ImportSpec) {
		for _, s := range f.SectionTemplates {
			if s.Name == "source-header" {
				codegen.AddImport(s, specs...)
				return
			}
		}
	}

	patchClient := func(f *codegen.File) {
		// Add errors import for structured error handling
		addImports(f, &codegen.ImportSpec{Path: "errors"})

		// Append JSONRPCError type definition
		f.SectionTemplates = append(f.SectionTemplates, &codegen.SectionTemplate{
			Name: "a2a-jsonrpc-error",
			Source: `
// JSONRPCError is a typed error for JSON-RPC error responses.
type JSONRPCError struct {
	Code    int
	Message string
	Data    any
}

func (e *JSONRPCError) Error() string {
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

// IsJSONRPCError checks if the error is a JSON-RPC error with the given code.
func IsJSONRPCError(err error, code int) bool {
	var jre *JSONRPCError
	if errors.As(err, &jre) {
		return jre.Code == code
	}
	return false
}
`,
		})
	}

	patchEncodeDecode := func(f *codegen.File) {
		for _, s := range f.SectionTemplates {
			// Add SSE Accept header for tasks/sendSubscribe streaming method
			if strings.Contains(s.Source, "func (c *Client) BuildTasksSendSubscribeRequest(") {
				s.Source = strings.Replace(s.Source, "return req, nil", "req.Header.Set(\"Accept\", \"text/event-stream\")\n\treturn req, nil", 1)
			}
			if strings.Contains(s.Source, "func EncodeTasksSendSubscribeRequest(") && strings.Contains(s.Source, "return func(req *http.Request, v any) error {") {
				s.Source = strings.Replace(s.Source, "return func(req *http.Request, v any) error {", "return func(req *http.Request, v any) error {\n\t\t// Request SSE stream for tasks/sendSubscribe\n\t\treq.Header.Set(\"Accept\", \"text/event-stream\")", 1)
			}
		}
	}

	patchStream := func(f *codegen.File) {
		for _, s := range f.SectionTemplates {
			// Replace generic error with structured JSONRPCError
			s.Source = strings.ReplaceAll(s.Source, "return zero, fmt.Errorf(\"JSON-RPC error %d: %s\", response.Error.Code, response.Error.Message)", "return zero, &JSONRPCError{Code: int(response.Error.Code), Message: response.Error.Message}")
			// Add context cancellation check in streaming loop
			s.Source = strings.Replace(s.Source, "for {\n\t\teventType, data, err := s.parseSSEEvent()", "for {\n\t\tselect {\n\t\tcase <-ctx.Done():\n\t\t\ts.closed = true\n\t\t\treturn zero, ctx.Err()\n\t\tdefault:\n\t\t}\n\t\teventType, data, err := s.parseSSEEvent()", 1)
		}
	}

	for _, f := range clientFiles {
		p := filepath.ToSlash(f.Path)
		switch p {
		case filepath.ToSlash(clientPath):
			patchClient(f)
		case filepath.ToSlash(encodeDecodePath):
			patchEncodeDecode(f)
		case filepath.ToSlash(streamPath):
			patchStream(f)
		}
	}
}
