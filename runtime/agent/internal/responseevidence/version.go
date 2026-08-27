// Package responseevidence defines the stable encoding version shared by the
// model boundary that computes evidence and the workflow API that persists it.
package responseevidence

const (
	// VersionV1 identifies the first stable rejected-response encoding.
	VersionV1 = "goa-ai.rejected-model-responses.v1"

	// VersionV2 adds the provider-neutral output-limit classification.
	VersionV2 = "goa-ai.rejected-model-responses.v2"
)
