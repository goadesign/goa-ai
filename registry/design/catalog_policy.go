// Catalog methods retain native declarations and enumerate saved resource owners.
// Hosts authenticate callers and decide which owners their applications may use.
package design

import (
	registrytypes "goa.design/goa-ai/registry/design/types"
	. "goa.design/goa/v3/dsl"
)

func catalogResourceMethods() {
	Method("CheckCatalogResource", func() {
		Description("Check whether a permanent resource has an active catalog registration without selecting a revision. This read returns catalog existence and retirement only; the host owns caller authorization.")
		Payload(registrytypes.CatalogResource)
		Error("not_found")
		Error("admission_retired")
		Error("service_unavailable")
		GRPC(func() {})
	})
	Method("ListCatalogResources", func() {
		Description("Read a bounded page of stable resources registered under one owning scope. Registry returns identities only; the resource owner supplies its current configuration. Each request examines at most limit indexed candidates and may return an empty page with more work remaining.")
		Payload(registrytypes.CatalogResourcesQuery)
		Result(registrytypes.CatalogResourcesPage)
		Error("validation_error")
		Error("service_unavailable")
		GRPC(func() {})
	})

	Method("RegisterAgentExports", func() {
		Description("Retain an accepted immutable native revision and its generated operation declarations in one write. Repetition preserves the original registration; changed content or retired exact registration conflicts. An unseen revision cannot reopen a permanently retired resource.")
		Payload(registrytypes.AgentExportsDeclaration)
		Result(ResolvedToolset)
		Error("validation_error")
		Error("admission_conflict")
		Error("admission_retired")
		Error("service_unavailable")
		GRPC(func() {})
	})
	Method("ValidateAgentExports", func() {
		Description("Validate generated native operation declarations and check that the permanent resource is not retired. The host validates dependencies before delivering the accepted revision.")
		Payload(registrytypes.AgentExportsValidation)
		Error("validation_error")
		Error("not_found")
		Error("admission_retired")
		Error("service_unavailable")
		GRPC(func() {})
	})
	Method("RetireAgentResource", func() {
		Description("Permanently disable new native revisions and catalog discovery for one resource while retaining saved declarations. Exact repetition succeeds and never reopens the resource; hosts authorize the management request.")
		Payload(registrytypes.CatalogResource)
		Error("validation_error")
		Error("service_unavailable")
		GRPC(func() {})
	})

}
