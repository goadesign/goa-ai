package bedrock

import (
	"fmt"
	"slices"
	"strings"
)

// geoScopes are the Bedrock cross-region inference-profile scope prefixes. A
// configured model ID may carry one of these before its vendor namespace (for
// example "us.anthropic.claude-opus-4-8"); the set is closed — these are the
// AWS geo scopes, not an open vocabulary.
var geoScopes = []string{"global", "us", "eu", "au", "jp", "apac", "us-gov"}

// vendorNamespaces are the leading segments of a Bedrock foundation model ID,
// which has the shape vendor.model[:version]. An ID that already begins with a
// vendor namespace carries no geo scope and is a foundation ID as written.
var vendorNamespaces = []string{
	"anthropic", "amazon", "meta", "cohere", "mistral", "ai21",
	"deepseek", "stability", "writer", "luma", "twelvelabs", "qwen", "openai",
}

// FoundationModelID resolves a configured Bedrock model reference to the
// foundation model ID that the Bedrock Runtime CountTokens API requires.
//
// Bedrock invokes cross-region models through an inference profile whose ID
// prepends a geo scope to the foundation model ID (for example
// "us.anthropic.claude-opus-4-8" backs the foundation model
// "anthropic.claude-opus-4-8"). Converse and ConverseStream accept the profile
// ID, but CountTokens is a foundation-model operation and rejects a profile ID
// with a ValidationException — so the profile must be translated to its
// foundation model ID before the count request is built.
//
// The translation keys on the leading dotted segment, using two closed sets —
// the AWS geo scopes and the known Bedrock vendor namespaces:
//
//   - a geo scope {global, us, eu, au, jp, apac, us-gov} is stripped, yielding
//     the foundation model ID;
//   - a vendor namespace {anthropic, amazon, meta, ...} means the ID is already
//     a foundation model ID and passes through unchanged;
//   - anything else fails fast: an unrecognized leading segment is neither a
//     scope this function can safely strip nor a namespace it can trust, so it
//     is rejected here rather than forwarded to Bedrock as a guaranteed
//     ValidationException.
func FoundationModelID(id string) (string, error) {
	prefix, rest, ok := strings.Cut(id, ".")
	if !ok {
		return "", fmt.Errorf(
			"bedrock: cannot resolve foundation model ID for %q: expected a vendor namespace (e.g. %q) or a geo-scoped inference profile",
			id, "anthropic.",
		)
	}
	if slices.Contains(geoScopes, prefix) {
		return rest, nil
	}
	if slices.Contains(vendorNamespaces, prefix) {
		return id, nil
	}
	return "", fmt.Errorf(
		"bedrock: cannot resolve foundation model ID for %q: unrecognized leading namespace %q (expected a geo scope %v or a vendor namespace %v)",
		id, prefix, geoScopes, vendorNamespaces,
	)
}
