package agent

import (
	"fmt"
	"net/url"
	"time"

	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	// RegistryExpr captures a registry source declaration from the agent DSL.
	// It represents a centralized catalog of MCP servers, toolsets, and agents
	// that can be discovered and consumed by goa-ai agents.
	RegistryExpr struct {
		eval.DSLFunc

		// Name is the unique identifier for this registry within the design.
		Name string
		// Description provides a human-readable explanation of the
		// registry's purpose.
		Description string
		// URL is the registry endpoint URL.
		URL string
		// APIVersion is the registry API version (e.g., "v1", "2024-11-05").
		// Defaults to "v1" for Anthropic-compatible registries.
		APIVersion string
		// Requirements contains the security requirements for authenticating
		// with this registry. References Goa security schemes.
		Requirements []*goaexpr.SecurityExpr
		// SyncInterval specifies how often to refresh the registry catalog.
		SyncInterval time.Duration
		// CacheTTL specifies local cache duration for registry data.
		CacheTTL time.Duration
		// Timeout specifies HTTP request timeout for registry operations.
		Timeout time.Duration
		// RetryPolicy configures retry behavior for failed requests.
		RetryPolicy *RetryPolicyExpr
		// Federation configures external registry import settings.
		Federation *FederationExpr
	}

	// RetryPolicyExpr defines retry configuration for registry operations.
	RetryPolicyExpr struct {
		// MaxRetries is the maximum number of retry attempts.
		MaxRetries int
		// BackoffBase is the initial backoff duration between retries.
		BackoffBase time.Duration
		// BackoffMax is the maximum backoff duration between retries.
		BackoffMax time.Duration
	}
)

// EvalName implements eval.Expression allowing descriptive error messages.
func (r *RegistryExpr) EvalName() string {
	return fmt.Sprintf("registry %q", r.Name)
}

// AddSecurityRequirement implements goaexpr.SecurityHolder, allowing the
// Security() DSL function to be used inside Registry declarations.
func (r *RegistryExpr) AddSecurityRequirement(sec *goaexpr.SecurityExpr) {
	r.Requirements = append(r.Requirements, sec)
}

// SetURL implements goaexpr.URLHolder, allowing the URL() DSL function
// to be used inside Registry declarations.
func (r *RegistryExpr) SetURL(u string) {
	r.URL = u
}

// Prepare sets default values for optional fields.
func (r *RegistryExpr) Prepare() {
	if r.APIVersion == "" {
		r.APIVersion = "v1"
	}
}

// Validate performs semantic checks on the registry expression.
func (r *RegistryExpr) Validate() error {
	verr := new(eval.ValidationErrors)

	if r.Name == "" {
		verr.Add(r, "registry name is required")
	}

	if r.URL == "" {
		verr.Add(r, "registry URL is required; use URL(\"https://...\") to set it")
	} else {
		if _, err := url.Parse(r.URL); err != nil {
			verr.Add(r, "invalid registry URL %q: %s", r.URL, err)
		}
	}

	if r.SyncInterval < 0 {
		verr.Add(r, "SyncInterval must be non-negative")
	}

	if r.CacheTTL < 0 {
		verr.Add(r, "CacheTTL must be non-negative")
	}

	if r.Timeout < 0 {
		verr.Add(r, "Timeout must be non-negative")
	}

	if r.RetryPolicy != nil {
		if r.RetryPolicy.MaxRetries < 0 {
			verr.Add(r, "RetryPolicy.MaxRetries must be non-negative")
		}
		if r.RetryPolicy.BackoffBase < 0 {
			verr.Add(r, "RetryPolicy.BackoffBase must be non-negative")
		}
		if r.RetryPolicy.BackoffMax < 0 {
			verr.Add(r, "RetryPolicy.BackoffMax must be non-negative")
		}
		if r.RetryPolicy.BackoffMax > 0 && r.RetryPolicy.BackoffBase > r.RetryPolicy.BackoffMax {
			verr.Add(r, "RetryPolicy.BackoffBase must not exceed BackoffMax")
		}
	}

	if len(verr.Errors) == 0 {
		return nil
	}
	return verr
}

// Finalize completes the registry expression after validation.
func (r *RegistryExpr) Finalize() {
	// Nothing to finalize currently; defaults are set in Prepare.
}

// SetTimeout implements expr.TimeoutHolder, allowing the Timeout() DSL
// function to set the registry timeout.
func (r *RegistryExpr) SetTimeout(duration string) error {
	d, err := time.ParseDuration(duration)
	if err != nil {
		return err
	}
	r.Timeout = d
	return nil
}
