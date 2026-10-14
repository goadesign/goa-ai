// Registry resources, exact operations and saved invocation inputs are portable
// owner contracts. Hosts authenticate callers before using these private methods;
// identities select retained resources and never act as credentials.
package types

import (
	"goa.design/goa-ai/runtime/toolregistry"
	. "goa.design/goa/v3/dsl"
)

var (
	// CatalogResource selects a permanent resource independently of its versions.
	CatalogResource = Type("CatalogResource", func() {
		Field(1, "kind", String, "Resource kind; equal names across kinds differ.", func() { Enum("agent", "service") })
		Field(2, "scope", String, "Owning scope authenticated by the host.", func() { MinLength(1) })
		Field(3, "name", String, "Permanent resource name within its owning scope.", func() { MinLength(1) })
		Required("kind", "scope", "name")
	})
	// NativeCatalogTarget selects one operation from an immutable native route.
	NativeCatalogTarget = Type("NativeCatalogTarget", func() {
		Field(1, "toolset", String, "Immutable native revision route assigned by its owner.", func() { MinLength(1) })
		Field(2, "tool", String, "Exact generated operation name.", func() { MinLength(1) })
		Required("toolset", "tool")
	})
	// ServiceCatalogTarget selects the exact service registration and operation.
	ServiceCatalogTarget = Type("ServiceCatalogTarget", func() {
		Field(1, "toolset", String, "Registry route of the exact service registration.", func() { MinLength(1) })
		Field(2, "tool", String, "Exact generated service operation name.", func() { MinLength(1) })
		Field(3, "registration_token", String, "Exact retained service registration.", func() { Pattern(toolregistry.RegistrationTokenPattern) })
		Required("toolset", "tool", "registration_token")
	})
	// CatalogOperation pairs the resource scope and name authorized by the host
	// with one selected operation.
	CatalogOperation = Type("CatalogOperation", func() {
		Field(1, "scope", String, "Owning scope supplied and authorized by the host.", func() { MinLength(1) })
		Field(2, "name", String, "Permanent resource name supplied and authorized by the host within that scope.", func() { MinLength(1) })
		OneOf("target", func() {
			TypeName("CatalogTarget")
			Field(3, "native", NativeCatalogTarget, "Operation of an immutable native revision.")
			Field(4, "service", ServiceCatalogTarget, "Operation of one exact service registration.")
		})
		Required("scope", "name", "target")
	})
	// AgentExportsDeclaration retains a complete immutable native revision.
	AgentExportsDeclaration = Type("AgentExportsDeclaration", func() {
		Field(1, "scope", String, "Owning scope.", func() { MinLength(1) })
		Field(2, "resource_name", String, "Permanent native resource name.", func() { MinLength(1) })
		Field(3, "name", String, "Immutable revision route, separate from the resource name.", func() { MinLength(1) })
		Field(4, "description", String, "Description of the revision.")
		Field(5, "version", SemVer, "Provider declaration version.")
		Field(6, "tags", ArrayOf(String), "Discovery tags; tags never grant permission.")
		Field(7, "exports", ArrayOfRequired(ToolSchema), "Complete generated native operation declarations.", func() { MinLength(1) })
		Required("scope", "resource_name", "name", "exports")
	})
	// AgentExportsValidation checks fresh intent before a revision exists.
	AgentExportsValidation = Type("AgentExportsValidation", func() {
		Field(1, "scope", String, "Owning scope.", func() { MinLength(1) })
		Field(2, "resource_name", String, "Permanent native resource name.", func() { MinLength(1) })
		Field(3, "exports", ArrayOfRequired(ToolSchema), "Proposed generated native operation declarations.", func() { MinLength(1) })
		Required("scope", "resource_name", "exports")
	})
	// RootInvocation identifies the original accepted root across workflow successors.
	RootInvocation = Type("RootInvocation", func() {
		Meta("struct:pkg:path", "registry")
		Field(1, "run_id", String, "Original globally unique accepted root Run ID.", func() { Meta("struct:tag:json", "run_id"); MinLength(1) })
		Required("run_id")
	})
	// ToolInvocation identifies a saved call across execution retries and input resumes.
	ToolInvocation = Type("ToolInvocation", func() {
		Meta("struct:pkg:path", "registry")
		Field(1, "run_id", String, "Run that originally issued this invocation.", func() { Meta("struct:tag:json", "run_id"); MinLength(1) })
		Field(2, "tool_call_id", String, "Runtime-owned original tool call ID; result paging creates another ID.", func() { Meta("struct:tag:json", "tool_call_id"); MinLength(1) })
		Required("run_id", "tool_call_id")
	})
	// ExecutionInvocation is the immutable invocation identity, not a transport sequence.
	ExecutionInvocation = Type("ExecutionInvocation", func() {
		Meta("struct:pkg:path", "registry")
		OneOf("invocation", func() {
			Meta("struct:tag:json", "invocation")
			TypeName("ExecutionInvocationKind")
			Field(1, "root", RootInvocation, "Independent accepted root.")
			Field(2, "tool", ToolInvocation, "Original saved tool invocation.")
		})
		Required("invocation")
	})
	// RegisteredInvocation retains the selected target and original input for a root run.
	RegisteredInvocation = Type("RegisteredInvocation", func() {
		Meta("struct:pkg:path", "registry")
		Meta("type:generate:force")
		Meta("openapi:generate", "false")
		Field(1, "invocation", ExecutionInvocation, "Original root or tool invocation.")
		Field(2, "target", CatalogOperation, "Exact immutable operation selected for this invocation.")
		Field(3, "arguments", Bytes, "Original operation input validated by the generated operation codec.", func() { MinLength(1) })
		Required("invocation", "target", "arguments")
	})
	// CatalogResourcesQuery bounds one current resource discovery request.
	CatalogResourcesQuery = Type("CatalogResourcesQuery", func() {
		Field(1, "scope", String, "Owning scope whose registered resources are selected.", func() { MinLength(1) })
		Field(2, "kind", String, "Selected resource kind.", func() { Enum("agent", "service") })
		Field(4, "after", String, "Registry continuation from the same owning scope, kind and name filter.", func() { MinLength(1) })
		Field(5, "limit", Int32, "Maximum indexed candidates examined in this request; returned resources may be fewer.", func() { Minimum(1) })
		Field(6, "name", String, "Optional exact stable resource name filter.", func() { MinLength(1) })
		Required("scope", "kind", "limit")
	})
	// ResourcePageDone ends a bounded resource read.
	ResourcePageDone = Type("ResourcePageDone", func() { Description("No indexed candidates remain at this read.") })
	// ResourcePageMore retains the next position for the same selection.
	ResourcePageMore = Type("ResourcePageMore", func() {
		Field(1, "after", String, "Continue this same selection after the last examined candidate.", func() { MinLength(1) })
		Required("after")
	})
	// CatalogResourcesPage separates current results from remaining indexed work.
	CatalogResourcesPage = Type("CatalogResourcesPage", func() {
		Field(1, "resources", ArrayOfRequired(CatalogResource), "Active resources registered under the requested owner; an empty resumable page is valid.")
		OneOf("continuation", func() {
			TypeName("ResourcePageContinuation")
			Field(2, "done", ResourcePageDone)
			Field(3, "more", ResourcePageMore)
		})
		Required("resources", "continuation")
	})
)
