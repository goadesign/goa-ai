package codegen

import (
	"path/filepath"
	"time"

	agentsExpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// RegistryClientData holds the template-ready data for generating a registry
	// client. Each declared Registry in the DSL produces one client package.
	RegistryClientData struct {
		// Name is the DSL-provided registry identifier.
		Name string
		// GoName is the exported Go identifier derived from Name.
		GoName string
		// Description is the DSL-provided description.
		Description string
		// URL is the registry endpoint URL.
		URL string
		// APIVersion is the registry API version (e.g., "v1").
		APIVersion string
		// PackageName is the Go package name for the generated client.
		PackageName string
		// Dir is the output directory for the client package.
		Dir string
		// ImportPath is the full Go import path to the client package.
		ImportPath string
		// Timeout is the HTTP request timeout.
		Timeout time.Duration
		// RetryPolicy contains retry configuration.
		RetryPolicy *RetryPolicyData
		// SyncInterval is how often to refresh the catalog.
		SyncInterval time.Duration
		// CacheTTL is the local cache duration.
		CacheTTL time.Duration
		// SecuritySchemes contains the security requirements.
		SecuritySchemes []*SecuritySchemeData
		// Federation contains federation configuration if present.
		Federation *FederationData
	}

	// RetryPolicyData holds retry configuration for code generation.
	RetryPolicyData struct {
		// MaxRetries is the maximum number of retry attempts.
		MaxRetries int
		// BackoffBase is the initial backoff duration.
		BackoffBase time.Duration
		// BackoffMax is the maximum backoff duration.
		BackoffMax time.Duration
	}

	// SecuritySchemeData holds security scheme information for code generation.
	SecuritySchemeData struct {
		// Name is the security scheme name.
		Name string
		// Kind is the Goa security scheme kind.
		Kind goaexpr.SchemeKind
		// In is where the credential is sent (e.g., "header", "query").
		In string
		// ParamName is the parameter name (e.g., "Authorization").
		ParamName string
		// Scopes lists required OAuth2 scopes.
		Scopes []string
	}

	// FederationData holds federation configuration for code generation.
	FederationData struct {
		// Include patterns for namespaces to import.
		Include []string
		// Exclude patterns for namespaces to skip.
		Exclude []string
	}
)

// registryClientFiles generates the registry client files for all declared
// registries. Each registry produces a client package under
// gen/<service>/registry/<name>/.
func registryClientFiles(genpkg string, svc *ServiceAgentsData) []*codegen.File {
	if svc == nil || svc.Service == nil {
		return nil
	}

	var files []*codegen.File
	for _, reg := range agentsExpr.Root.Registries {
		if reg == nil {
			continue
		}
		data := newRegistryClientData(genpkg, svc.Service.PathName, reg)
		if data == nil {
			continue
		}

		// Generate client.go
		clientFile := registryClientFile(data)
		if clientFile != nil {
			files = append(files, clientFile)
		}

		// Generate options.go
		optionsFile := registryClientOptionsFile(data)
		if optionsFile != nil {
			files = append(files, optionsFile)
		}
	}
	return files
}

// newRegistryClientData transforms a RegistryExpr into template-ready data.
func newRegistryClientData(genpkg, svcPath string, reg *agentsExpr.RegistryExpr) *RegistryClientData {
	if reg == nil {
		return nil
	}

	goName := codegen.Goify(reg.Name, true)
	pkgName := codegen.SnakeCase(reg.Name)
	dir := filepath.Join("gen", svcPath, "registry", pkgName)
	importPath := joinImportPath(genpkg, filepath.Join(svcPath, "registry", pkgName))

	data := &RegistryClientData{
		Name:         reg.Name,
		GoName:       goName,
		Description:  reg.Description,
		URL:          reg.URL,
		APIVersion:   reg.APIVersion,
		PackageName:  pkgName,
		Dir:          dir,
		ImportPath:   importPath,
		Timeout:      reg.Timeout,
		SyncInterval: reg.SyncInterval,
		CacheTTL:     reg.CacheTTL,
	}

	// Convert retry policy
	if reg.RetryPolicy != nil {
		data.RetryPolicy = &RetryPolicyData{
			MaxRetries:  reg.RetryPolicy.MaxRetries,
			BackoffBase: reg.RetryPolicy.BackoffBase,
			BackoffMax:  reg.RetryPolicy.BackoffMax,
		}
	}

	// Convert security schemes
	for _, sec := range reg.Requirements {
		for _, scheme := range sec.Schemes {
			if scheme.Kind == goaexpr.NoKind {
				// Skip schemes with no kind specified.
				continue
			}
			schemeData := &SecuritySchemeData{
				Name: scheme.SchemeName,
				Kind: scheme.Kind,
			}
			switch scheme.Kind {
			case goaexpr.APIKeyKind:
				schemeData.In = scheme.In
				schemeData.ParamName = scheme.Name
			case goaexpr.OAuth2Kind:
				schemeData.Scopes = sec.Scopes
			case goaexpr.JWTKind:
				schemeData.In = "header"
				schemeData.ParamName = "Authorization"
			case goaexpr.BasicAuthKind:
				schemeData.In = "header"
				schemeData.ParamName = "Authorization"
			case goaexpr.NoKind:
				// Already handled above
			}
			data.SecuritySchemes = append(data.SecuritySchemes, schemeData)
		}
	}

	// Convert federation
	if reg.Federation != nil {
		data.Federation = &FederationData{
			Include: reg.Federation.Include,
			Exclude: reg.Federation.Exclude,
		}
	}

	return data
}

// registryClientFile generates the main client.go file for a registry.
func registryClientFile(data *RegistryClientData) *codegen.File {
	if data == nil {
		return nil
	}

	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "encoding/json"},
		{Path: "errors"},
		{Path: "fmt"},
		{Path: "io"},
		{Path: "net/http"},
		{Path: "net/url"},
		{Path: "time"},
		{Path: "goa.design/goa-ai/runtime/registry", Name: "registry"},
	}

	sections := []*codegen.SectionTemplate{
		codegen.Header(data.GoName+" registry client", data.PackageName, imports),
		{
			Name:    "registry-client",
			Source:  agentsTemplates.Read(registryClientFileT),
			Data:    data,
			FuncMap: templateFuncMap(),
		},
	}

	return &codegen.File{
		Path:             filepath.Join(data.Dir, "client.go"),
		SectionTemplates: sections,
	}
}

// registryClientOptionsFile generates the options.go file for a registry client.
func registryClientOptionsFile(data *RegistryClientData) *codegen.File {
	if data == nil {
		return nil
	}

	imports := []*codegen.ImportSpec{
		{Path: "net/http"},
		{Path: "time"},
	}

	sections := []*codegen.SectionTemplate{
		codegen.Header(data.GoName+" registry client options", data.PackageName, imports),
		{
			Name:    "registry-client-options",
			Source:  agentsTemplates.Read(registryClientOptionsFileT),
			Data:    data,
			FuncMap: templateFuncMap(),
		},
	}

	return &codegen.File{
		Path:             filepath.Join(data.Dir, "options.go"),
		SectionTemplates: sections,
	}
}
