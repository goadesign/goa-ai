package tools

import "strings"

// Ident is the strong type for globally unique tool identifiers.
// Tool IDs are canonical strings of the form "toolset.tool". Use this type
// in maps and APIs to avoid mixing with free-form strings and to document
// intent at call sites.
type Ident string

// String returns the string representation of the identifier.
func (id Ident) String() string {
	return string(id)
}

// Toolset returns the toolset component of the identifier.
func (id Ident) Toolset() string {
	parts := strings.Split(string(id), ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}

// Tool returns the tool name component of the identifier.
func (id Ident) Tool() string {
	parts := strings.Split(string(id), ".")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
