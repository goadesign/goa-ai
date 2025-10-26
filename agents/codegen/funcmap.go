package codegen

import (
	goacodegen "goa.design/goa/v3/codegen"
)

// templateFuncMap returns the set of helper functions made available to the
// code generation templates. We keep the definition centralized so all sections
// share the same helpers (e.g., goify for consistent identifier casing).
func templateFuncMap() map[string]any {
	return map[string]any{
		"goify": goacodegen.Goify,
	}
}
