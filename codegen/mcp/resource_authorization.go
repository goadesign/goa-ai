// Package codegen reads native Goa security expressions and plans the required MCP
// resource verifier. It computes complete scope alternatives during generation;
// HTTP requests only select an authored operation. Original service endpoints
// retain their authentication functions, middleware and domain authorization.
package codegen

import (
	"cmp"
	"fmt"
	"slices"

	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/generator"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// resourcePolicy keeps static scope facts and the native resource scheme.
	resourcePolicy struct {
		// Scheme identifies the one authored resource credential owner.
		Scheme *expr.SchemeExpr
		// BasicScopes contains alternatives for catalogs and other basic requests.
		BasicScopes [][]string
		// Tools contains tool-specific alternatives beyond basic access.
		Tools map[string][][]string
		// Resources selects fixed resource readers by their exact URI.
		Resources map[string][][]string
		// ResourceReader supplies the declared URI reader's alternatives.
		ResourceReader [][]string
		// Prompts selects method-backed prompts by their declared name.
		Prompts map[string][][]string
		// Completions selects the declared reference and argument provider.
		Completions []*completionScopePolicy
		// Catalogs supplies the scope alternatives of each configured catalog method.
		Catalogs map[string][][]string
		// Subscription contains scopes for the resource update stream.
		Subscription [][]string
		// Operations selects protocol methods that need typed request decoding.
		Operations map[string]bool
		// SelectScopesDeclaration retains the private scope function's Go name.
		SelectScopesDeclaration *codegen.NameDeclaration
	}
	// completionScopePolicy identifies one authored suggestion operation.
	completionScopePolicy struct {
		// Type, Reference and Argument identify one authored suggestion provider.
		Type, Reference, Argument string
		// Scopes contains the provider's complete resource scope alternatives.
		Scopes [][]string
	}
	// resourceFactoryOrder gives each generated example factory a stable owner.
	resourceFactoryOrder string
)

// resolveResourcePolicy selects an explicit MCP policy or an inherited bearer
// resource policy. API authentication cannot silently disappear from catalogs;
// unsupported API policies fail generation rather than produce a public route.
func resolveResourcePolicy(root *expr.RootExpr, service *expr.ServiceExpr, mcp *mcpexpr.MCPExpr) (*resourcePolicy, error) {
	requirements := mcp.Requirements
	if len(requirements) == 0 && bearerResourceRequirements(service.Requirements) {
		requirements = service.Requirements
	}
	if len(requirements) == 0 {
		requirements = root.API.Requirements
	}
	requirements = expr.EffectiveSecurityRequirements(requirements)
	if len(requirements) == 0 {
		return nil, nil
	}
	policy := &resourcePolicy{
		Tools: make(map[string][][]string), Resources: make(map[string][][]string),
		Prompts: make(map[string][][]string), Operations: make(map[string]bool),
	}
	for _, requirement := range requirements {
		if !bearerResourceRequirements([]*expr.SecurityExpr{requirement}) {
			return nil, fmt.Errorf("MCP service %q resource security requires one OAuth2, Bearer or JWT scheme per alternative; declare resource Security inside MCP and keep other credentials on original methods", service.Name)
		}
		scheme := requirement.Schemes[0].AuthoredScheme()
		if policy.Scheme != nil && policy.Scheme != scheme {
			return nil, fmt.Errorf("MCP service %q resource alternatives must use the same authored bearer scheme", service.Name)
		}
		policy.Scheme = scheme
		for _, scope := range scheme.Scopes {
			if !resourceScopeToken(scope.Name) {
				return nil, fmt.Errorf("MCP service %q bearer scope %q must use the OAuth scope grammar", service.Name, scope.Name)
			}
		}
		for _, scope := range requirement.Scopes {
			if !slices.ContainsFunc(scheme.Scopes, func(declared *expr.ScopeExpr) bool { return declared.Name == scope }) || !resourceScopeToken(scope) {
				return nil, fmt.Errorf("MCP service %q resource scope %q must be declared by its bearer scheme and use the OAuth scope grammar", service.Name, scope)
			}
		}
		policy.BasicScopes = append(policy.BasicScopes, slices.Clone(requirement.Scopes))
	}
	for _, tool := range mcp.Tools {
		scopes := operationResourceScopes(root, service, policy, tool.Method)
		if !slices.EqualFunc(scopes, policy.BasicScopes, slices.Equal[[]string]) {
			policy.Tools[tool.Name] = scopes
			policy.Operations["tools/call"] = true
		}
	}
	for _, resource := range mcp.Resources {
		scopes := operationResourceScopes(root, service, policy, resource.Method)
		if !slices.EqualFunc(scopes, policy.BasicScopes, slices.Equal[[]string]) {
			policy.Resources[resource.URI] = scopes
			policy.Operations["resources/read"] = true
		}
	}
	if len(mcp.ResourceTemplates) > 0 {
		scopes := operationResourceScopes(root, service, policy, mcp.ResourceTemplates[0].Method)
		if !slices.EqualFunc(scopes, policy.BasicScopes, slices.Equal[[]string]) {
			policy.ResourceReader = scopes
			policy.Operations["resources/read"] = true
			for _, resource := range mcp.Resources {
				policy.Resources[resource.URI] = operationResourceScopes(root, service, policy, resource.Method)
			}
		}
	}
	for _, prompt := range mcp.MethodPrompts {
		scopes := operationResourceScopes(root, service, policy, prompt.Method)
		if !slices.EqualFunc(scopes, policy.BasicScopes, slices.Equal[[]string]) {
			policy.Prompts[prompt.Name] = scopes
			policy.Operations["prompts/get"] = true
		}
	}
	for _, completion := range mcp.PromptCompletions {
		scopes := operationResourceScopes(root, service, policy, completion.Method)
		if !slices.EqualFunc(scopes, policy.BasicScopes, slices.Equal[[]string]) {
			policy.Completions = append(policy.Completions, &completionScopePolicy{Type: "ref/prompt", Reference: completion.Prompt, Argument: completion.Argument, Scopes: scopes})
			policy.Operations["completion/complete"] = true
		}
	}
	for _, completion := range mcp.ResourceCompletions {
		scopes := operationResourceScopes(root, service, policy, completion.Method)
		if !slices.EqualFunc(scopes, policy.BasicScopes, slices.Equal[[]string]) {
			policy.Completions = append(policy.Completions, &completionScopePolicy{Type: "ref/resource", Reference: completion.URI, Argument: completion.Argument, Scopes: scopes})
			policy.Operations["completion/complete"] = true
		}
	}
	for _, catalog := range []struct {
		method    *expr.MethodExpr
		operation string
	}{
		{mcp.ToolCatalog, "tools/list"},
		{mcp.PromptCatalog, "prompts/list"},
	} {
		if catalog.method == nil {
			continue
		}
		scopes := operationResourceScopes(root, service, policy, catalog.method)
		if !slices.EqualFunc(scopes, policy.BasicScopes, slices.Equal[[]string]) {
			if policy.Catalogs == nil {
				policy.Catalogs = make(map[string][][]string)
			}
			policy.Catalogs[catalog.operation] = scopes
		}
	}
	if mcp.SubscriptionSource != nil {
		scopes := operationResourceScopes(root, service, policy, mcp.SubscriptionSource.Method)
		if !slices.EqualFunc(scopes, policy.BasicScopes, slices.Equal[[]string]) {
			policy.Subscription = scopes
		}
	}
	return policy, nil
}

// planResourceAuthorization requires the same concrete verifier in the native
// server constructor and its generated example. Unprotected MCP services emit
// neither the dependency nor resource authorization branches.
func planResourceAuthorization(plan *generator.Plan, prepared *preparedMCPService) error {
	if prepared.resourcePolicy == nil {
		return nil
	}
	transport, exists := plan.JSONRPC(prepared.root)
	if !exists {
		return fmt.Errorf("protected MCP service %q has no native JSON-RPC plan", prepared.userService.Name)
	}
	layout, err := codegen.PlanGoType(&expr.AttributeExpr{
		Type: expr.String,
		Meta: expr.MetaExpr{"struct:field:type": {"*mcpruntime.ResourceServer", "goa.design/goa-ai/runtime/mcp", "mcpruntime"}},
	}, codegen.GoTypePlanOptions{Owner: plan.Generation().GenPkg()})
	if err != nil {
		return err
	}
	for _, service := range prepared.root.API.JSONRPC.Services {
		if service.ServiceExpr != prepared.mcpService {
			continue
		}
		_, err := transport.DeclareServerConstructorDependency(service, "resourceServer", layout, "New"+codegen.Goify(prepared.userService.Name, true)+"ResourceServer", resourceFactoryOrder(prepared.userService.Name))
		if err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("protected MCP service %q is missing its generated transport", prepared.userService.Name)
}

// validateResourceCredentials prevents two credential owners from occupying
// Authorization. Independent domain keys keep their native header, query or
// cookie; the selected resource scheme keeps its original Goa callback.
func validateResourceCredentials(root *expr.RootExpr, service *expr.ServiceExpr, policy *resourcePolicy, credentials map[string][]*credentialInput) error {
	if policy == nil {
		return nil
	}
	for _, method := range service.Methods {
		for _, credential := range credentials[method.Name] {
			if credential.location != credentialHeaderLocation || credential.transportName != credentialAuthorizationHeader {
				continue
			}
			owned := false
			for _, requirement := range nativeCredentialRequirements(root, service, method) {
				for _, scheme := range requirement.Schemes {
					data := goaservice.BuildSchemeData(scheme, method)
					if scheme.AuthoredScheme() == policy.Scheme && data != nil && data.KeyAttr == credential.Name {
						owned = true
					}
				}
			}
			if !owned {
				return fmt.Errorf("MCP method %q credential %q shares Authorization with a different resource owner; use a distinct native binding for independent domain credentials", method.Name, credential.Name)
			}
		}
	}
	return nil
}

// operationResourceScopes computes all valid combinations of resource access
// and original method requirements. Non-resource authentication remains owned
// by the original endpoint and does not add unrelated scopes to an OAuth grant.
func operationResourceScopes(root *expr.RootExpr, service *expr.ServiceExpr, policy *resourcePolicy, method *expr.MethodExpr) [][]string {
	requirements := nativeCredentialRequirements(root, service, method)
	if len(requirements) == 0 {
		return policy.BasicScopes
	}
	var alternatives [][]string
	for _, basic := range policy.BasicScopes {
		for _, requirement := range requirements {
			scopes := slices.Clone(basic)
			for _, scheme := range requirement.Schemes {
				if scheme.AuthoredScheme() != policy.Scheme {
					continue
				}
				for _, scope := range requirement.Scopes {
					if !slices.Contains(scopes, scope) {
						scopes = append(scopes, scope)
					}
				}
			}
			if !slices.ContainsFunc(alternatives, func(existing []string) bool { return slices.Equal(existing, scopes) }) {
				alternatives = append(alternatives, scopes)
			}
		}
	}
	return alternatives
}

// bearerResourceRequirements identifies policies with one bearer scheme per
// alternative. Passwords and API keys cannot become OAuth resource credentials.
func bearerResourceRequirements(requirements []*expr.SecurityExpr) bool {
	if len(requirements) == 0 {
		return false
	}
	for _, requirement := range requirements {
		if len(requirement.Schemes) != 1 {
			return false
		}
		switch requirement.Schemes[0].Kind {
		case expr.OAuth2Kind, expr.BearerKind, expr.JWTKind:
		case expr.BasicAuthKind, expr.APIKeyKind, expr.NoKind:
			return false
		}
	}
	return true
}

// resourceScopeToken checks authored scope names against OAuth's ASCII grammar.
// Scope names cannot add whitespace or quoting syntax to an HTTP challenge.
func resourceScopeToken(scope string) bool {
	if scope == "" {
		return false
	}
	for _, value := range scope {
		if value < 0x21 || value > 0x7e || value == '"' || value == '\\' {
			return false
		}
	}
	return true
}

// ComparePackageName orders factories with the same preferred name by service.
func (order resourceFactoryOrder) ComparePackageName(other codegen.PackageNameOrder) int {
	return cmp.Compare(string(order), string(other.(resourceFactoryOrder)))
}
