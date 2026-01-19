package codegen

import (
	"strings"

	goaexpr "goa.design/goa/v3/expr"
)

// stripStructPkgMeta returns a shallow copy of att with struct:pkg:* locator
// metadata removed so synthesized local alias user types are treated as local
// to the tool specs package.
func stripStructPkgMeta(att *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
	if att == nil {
		return nil
	}
	out := *att
	if len(att.Meta) == 0 {
		return &out
	}
	meta := make(map[string][]string, len(att.Meta))
	for k, v := range att.Meta {
		// Drop locator metadata so synthesized local alias user types are treated
		// as local to the tool package (unqualified), even when the underlying
		// attribute graph was placed via struct:pkg:path.
		if strings.HasPrefix(k, "struct:pkg:") {
			continue
		}
		meta[k] = v
	}
	out.Meta = meta
	return &out
}
