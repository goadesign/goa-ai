// Package jsonshape renders direct value checks shared by generated tool and value codecs.
package jsonshape

import _ "embed"

// ValidatorsSource defines the template invoked as json-value-validators. Callers
// provide generated error adapters; the checks have no tool or runtime dependency.
//
//go:embed validators.go.tpl
var ValidatorsSource string
