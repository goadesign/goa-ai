package dsl

import (
	"time"

	agentsexpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/eval"
)

// Registry declares a registry source for tool discovery. Registries are
// centralized catalogs of MCP servers, toolsets, and agents that can be
// discovered and consumed by goa-ai agents.
//
// Registry must appear at the top level of a design.
//
// Registry takes a name and an optional DSL function to configure the registry:
//   - name: unique identifier for this registry within the design
//   - fn: optional configuration function
//
// Inside the DSL function, use:
//   - URL: sets the registry endpoint URL (required)
//   - Description: sets a human-readable description
//   - APIVersion: sets the registry API version (defaults to "v1")
//   - Security: references Goa security schemes for authentication
//   - RegistryTimeout: sets HTTP request timeout
//   - Retry: configures retry policy for failed requests
//   - SyncInterval: sets how often to refresh the catalog
//   - CacheTTL: sets local cache duration
//   - Federation: configures external registry import settings
//
// Example:
//
//	var CorpRegistry = Registry("corp-registry", func() {
//	    Description("Corporate tool registry")
//	    URL("https://registry.corp.internal")
//	    APIVersion("v1")
//	    Security(CorpAPIKey)
//	    RegistryTimeout("30s")
//	    Retry(3, "1s")
//	    SyncInterval("5m")
//	    CacheTTL("1h")
//	})
//
// Example with federation:
//
//	var AnthropicRegistry = Registry("anthropic", func() {
//	    Description("Anthropic MCP Registry")
//	    URL("https://registry.anthropic.com/v1")
//	    Security(AnthropicOAuth)
//	    Federation(func() {
//	        Include("web-search", "code-execution")
//	        Exclude("experimental/*")
//	    })
//	    SyncInterval("1h")
//	    CacheTTL("24h")
//	})
func Registry(name string, fn ...func()) *agentsexpr.RegistryExpr {
	if name == "" {
		eval.ReportError("registry name must be non-empty")
		return nil
	}
	if _, ok := eval.Current().(eval.TopExpr); !ok {
		eval.IncompatibleDSL()
		return nil
	}
	var dsl func()
	if len(fn) > 0 {
		dsl = fn[0]
	}
	reg := &agentsexpr.RegistryExpr{
		Name:    name,
		DSLFunc: dsl,
	}
	agentsexpr.Root.Registries = append(agentsexpr.Root.Registries, reg)
	return reg
}

// APIVersion sets the registry API version. This is used as a path segment
// in registry API calls (e.g., "v1", "2024-11-05").
//
// APIVersion must appear in a Registry expression.
//
// If not specified, defaults to "v1".
//
// Example:
//
//	Registry("corp", func() {
//	    URL("https://registry.corp.internal")
//	    APIVersion("v1")
//	})
func APIVersion(version string) {
	reg, ok := eval.Current().(*agentsexpr.RegistryExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	reg.APIVersion = version
}

// RegistryTimeout sets the HTTP request timeout for registry operations.
//
// RegistryTimeout must appear in a Registry expression.
//
// RegistryTimeout takes a duration string (e.g., "30s", "1m", "500ms").
//
// Example:
//
//	Registry("corp", func() {
//	    URL("https://registry.corp.internal")
//	    RegistryTimeout("30s")
//	})
func RegistryTimeout(duration string) {
	reg, ok := eval.Current().(*agentsexpr.RegistryExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	d, err := time.ParseDuration(duration)
	if err != nil {
		eval.ReportError("invalid timeout duration %q: %s", duration, err)
		return
	}
	reg.Timeout = d
}

// Retry configures the retry policy for failed registry requests.
//
// Retry must appear in a Registry expression.
//
// Retry takes:
//   - maxRetries: maximum number of retry attempts
//   - backoff: initial backoff duration between retries (e.g., "1s", "500ms")
//
// Example:
//
//	Registry("corp", func() {
//	    URL("https://registry.corp.internal")
//	    Retry(3, "1s")
//	})
func Retry(maxRetries int, backoff string) {
	reg, ok := eval.Current().(*agentsexpr.RegistryExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	d, err := time.ParseDuration(backoff)
	if err != nil {
		eval.ReportError("invalid retry backoff duration %q: %s", backoff, err)
		return
	}
	reg.RetryPolicy = &agentsexpr.RetryPolicyExpr{
		MaxRetries:  maxRetries,
		BackoffBase: d,
	}
}

// SyncInterval sets how often to refresh the registry catalog.
//
// SyncInterval must appear in a Registry expression.
//
// SyncInterval takes a duration string (e.g., "5m", "1h", "30s").
//
// Example:
//
//	Registry("corp", func() {
//	    URL("https://registry.corp.internal")
//	    SyncInterval("5m")
//	})
func SyncInterval(duration string) {
	reg, ok := eval.Current().(*agentsexpr.RegistryExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	d, err := time.ParseDuration(duration)
	if err != nil {
		eval.ReportError("invalid sync interval duration %q: %s", duration, err)
		return
	}
	reg.SyncInterval = d
}

// CacheTTL sets the local cache duration for registry data.
//
// CacheTTL must appear in a Registry expression.
//
// CacheTTL takes a duration string (e.g., "1h", "24h", "30m").
//
// Example:
//
//	Registry("corp", func() {
//	    URL("https://registry.corp.internal")
//	    CacheTTL("1h")
//	})
func CacheTTL(duration string) {
	reg, ok := eval.Current().(*agentsexpr.RegistryExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	d, err := time.ParseDuration(duration)
	if err != nil {
		eval.ReportError("invalid cache TTL duration %q: %s", duration, err)
		return
	}
	reg.CacheTTL = d
}

// Federation configures external registry import settings. Use Federation
// inside a Registry declaration to specify which namespaces to import from
// a federated source.
//
// Federation must appear in a Registry expression.
//
// Inside the Federation DSL function, use:
//   - Include: specifies glob patterns for namespaces to import
//   - Exclude: specifies glob patterns for namespaces to skip
//
// Example:
//
//	Registry("anthropic", func() {
//	    URL("https://registry.anthropic.com/v1")
//	    Federation(func() {
//	        Include("web-search", "code-execution")
//	        Exclude("experimental/*")
//	    })
//	})
func Federation(fn func()) {
	reg, ok := eval.Current().(*agentsexpr.RegistryExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if reg.Federation == nil {
		reg.Federation = &agentsexpr.FederationExpr{}
	}
	eval.Execute(fn, reg.Federation)
}

// Include specifies glob patterns for namespaces to import from a federated
// registry source. If no Include patterns are specified, all namespaces are
// included by default.
//
// Include must appear in a Federation expression.
//
// Include takes a variadic list of glob patterns.
//
// Example:
//
//	Federation(func() {
//	    Include("web-search", "code-execution", "data-*")
//	})
func Include(patterns ...string) {
	fed, ok := eval.Current().(*agentsexpr.FederationExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	fed.Include = append(fed.Include, patterns...)
}

// Exclude specifies glob patterns for namespaces to skip from a federated
// registry source. Exclude patterns are applied after Include patterns.
//
// Exclude must appear in a Federation expression.
//
// Exclude takes a variadic list of glob patterns.
//
// Example:
//
//	Federation(func() {
//	    Include("*")
//	    Exclude("experimental/*", "deprecated/*")
//	})
func Exclude(patterns ...string) {
	fed, ok := eval.Current().(*agentsexpr.FederationExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	fed.Exclude = append(fed.Exclude, patterns...)
}

// PublishTo configures registry publication for an exported toolset. Use
// PublishTo inside an Export DSL to specify which registries the toolset
// should be published to.
//
// PublishTo must appear in a Toolset expression that is being exported.
//
// PublishTo takes a registry expression returned by Registry().
//
// Example:
//
//	var CorpRegistry = Registry("corp", func() {
//	    URL("https://registry.corp.internal")
//	})
//
//	var LocalTools = Toolset("utils", func() {
//	    Tool("summarize", "Summarize text", func() {
//	        Args(func() { Attribute("text", String) })
//	        Return(func() { Attribute("summary", String) })
//	    })
//	})
//
//	Agent("data-agent", "Data processing agent", func() {
//	    Use(LocalTools)
//	    Export(LocalTools, func() {
//	        PublishTo(CorpRegistry)
//	        Tags("data", "etl")
//	    })
//	})
func PublishTo(registry *agentsexpr.RegistryExpr) {
	ts, ok := eval.Current().(*agentsexpr.ToolsetExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if registry == nil {
		eval.ReportError("PublishTo requires a non-nil registry")
		return
	}
	ts.PublishTo = append(ts.PublishTo, registry)
}
