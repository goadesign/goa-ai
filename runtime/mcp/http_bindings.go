// Package mcp consumes HTTP bindings emitted from Goa's evaluated design.
// Tool facts control headers and trusted retries. Credential query names identify
// authentication inputs that accompany a request without changing the protected
// resource address. Imported callers have no native credential query bindings.
package mcp

import (
	"maps"
	"net/url"
	"slices"
	"strings"
)

type (
	// HTTPBindings describes one generated service's HTTP inputs and tool behavior.
	// Construct it from the evaluated design, never from model arguments or a remote
	// catalog's extension values. NewHTTPTransport takes an independent copy.
	HTTPBindings struct {
		// Tools supplies header mappings and behavior hints for each tool name.
		Tools map[string]ToolBinding
		// CredentialQueries lists native authentication query names by protocol method.
		// These fields are delivered to the resource but excluded from its OAuth
		// identifier. All other URL components retain their exact identity.
		CredentialQueries map[string][]string
	}
	// ToolBinding describes the static or discovered HTTP behavior of one tool.
	// Generated clients provide these facts directly; imported clients derive them
	// from the catalog returned under the call's current authorization context.
	ToolBinding struct {
		// Headers maps tool argument properties to protocol request headers.
		Headers []HeaderBinding
		// ReadOnly states that the tool does not change its environment.
		ReadOnly bool
		// Idempotent states that repeating arguments has no additional effects.
		Idempotent bool
	}
)

// copyBindings separates constructor-owned mappings from subsequent caller edits.
// Header field paths and credential names stay fixed for every request sent by
// this transport, including requests running concurrently.
func copyBindings(bindings HTTPBindings) HTTPBindings {
	bindings.Tools = maps.Clone(bindings.Tools)
	for name, tool := range bindings.Tools {
		tool.Headers = slices.Clone(tool.Headers)
		for i := range tool.Headers {
			tool.Headers[i].Path = slices.Clone(tool.Headers[i].Path)
		}
		bindings.Tools[name] = tool
	}
	bindings.CredentialQueries = maps.Clone(bindings.CredentialQueries)
	for method, names := range bindings.CredentialQueries {
		bindings.CredentialQueries[method] = slices.Clone(names)
	}
	return bindings
}

// matchesResourceAddress excludes only declared authentication query fields.
// It preserves every other query byte, escaped path and empty query marker.
// A configured resource containing a credential field is rejected so discovery
// and token grants cannot disclose that credential to an authorization server.
func matchesResourceAddress(resource, address *url.URL, credentials, resourceCredentials []string) bool {
	configured, valid := withoutCredentialQuery(resource.RawQuery, resourceCredentials)
	if !valid || configured != resource.RawQuery {
		return false
	}
	query, valid := withoutCredentialQuery(address.RawQuery, credentials)
	if !valid {
		return false
	}
	projected := *address
	projected.RawQuery = query
	return projected.String() == resource.String()
}

// withoutCredentialQuery reads external query names using the same URL rules as
// Goa's native decoder. It removes declared authentication inputs while retaining
// all other segments in their original order and spelling. Invalid names or
// credential values fail before the transport obtains an access token.
func withoutCredentialQuery(query string, credentials []string) (string, bool) {
	parts := strings.Split(query, "&")
	remaining := make([]string, 0, len(parts))
	for _, part := range parts {
		name, _, _ := strings.Cut(part, "=")
		decoded, err := url.QueryUnescape(name)
		if err != nil {
			return "", false
		}
		if slices.Contains(credentials, decoded) {
			if _, err := url.ParseQuery(part); err != nil {
				return "", false
			}
			continue
		}
		remaining = append(remaining, part)
	}
	return strings.Join(remaining, "&"), true
}
