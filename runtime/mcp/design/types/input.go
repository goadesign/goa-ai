// Package types declares host questions shared by MCP adapters and public run
// input. Service state remains private; these values contain only the question
// the host presents. The MCP parser owns omitted-mode defaults and extensions.
package types

import (
	"goa.design/goa-ai/internal/mcpvalue"
	. "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/expr"
)

var (
	// FormParams describes a non-sensitive form using its original JSON schema.
	FormParams = mcpvalue.Define("HostFormParams", "hostinput", publicFormParams(), nil)

	// URLParams describes an external interaction that needs explicit consent.
	URLParams = mcpvalue.Define("HostURLParams", "hostinput", mcpvalue.URLParams(), nil)

	// InputRequest selects the validated form or URL question presented by a host.
	InputRequest = Type("HostInputRequest", func() {
		Meta("struct:pkg:path", "hostinput")
		OneOf("question", func() {
			TypeName("HostQuestion")
			Field(1, "form", FormParams, "Form input requested by the service.")
			Field(2, "url", URLParams, "External consent requested by the service.")
		})
		Field(3, "extensions", MapOf(String, Bytes), "Unknown original request parameters, keyed by their exact names. Each value contains its exact JSON bytes, including nested values and numbers.")
		Required("question")
	})
)

// publicFormParams retains the shared form fields while carrying the open JSON
// schema as exact bytes across service transports. MCP keeps its JSON-object wire
// representation through the original shared declaration.
func publicFormParams() *expr.AttributeExpr {
	params := mcpvalue.FormParams()
	schema := params.Find("requestedSchema")
	schema.Type = expr.Bytes
	schema.Description = "Exact JSON-object bytes of the original flat form schema."
	delete(schema.Meta, "struct:field:type")
	return params
}
