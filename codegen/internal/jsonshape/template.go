// Package jsonshape renders direct value checks shared by generated tool and value codecs.
package jsonshape

import _ "embed"

// Names supplies the owning package's helper declarations and import aliases to
// the shared validator template. Each producer resolves its own names.
type Names struct {
	// InvalidFieldType, UnknownField, DecodedType and ChildPath name the adapters.
	InvalidFieldType, UnknownField, DecodedType, ChildPath string
	// JSON, Fmt, Sort and Strconv are the imported package qualifiers.
	JSON, Fmt, Sort, Strconv string
}

// ValidatorsSource defines the template invoked as json-value-validators. Callers
// provide generated error adapters; the checks have no tool or runtime dependency.
//
//go:embed validators.go.tpl
var ValidatorsSource string
