package shared

import (
	"path/filepath"
	"sort"
	"strings"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

// JoinImportPath constructs a full import path by joining the generation package
// base path with a relative path. It handles trailing "/gen" suffixes correctly.
func JoinImportPath(genpkg, rel string) string {
	if rel == "" {
		return ""
	}
	base := strings.TrimSuffix(genpkg, "/")
	for strings.HasSuffix(base, "/gen") {
		base = strings.TrimSuffix(base, "/gen")
	}
	return filepath.ToSlash(filepath.Join(base, "gen", rel))
}

// GatherAttributeImports collects import specifications for external user types
// and meta-type imports referenced by the given attribute expression.
func GatherAttributeImports(genpkg string, att *expr.AttributeExpr) []*codegen.ImportSpec {
	uniq := make(map[string]*codegen.ImportSpec)
	var visit func(*expr.AttributeExpr)
	visit = func(a *expr.AttributeExpr) {
		if a == nil {
			return
		}
		for _, im := range codegen.GetMetaTypeImports(a) {
			if im != nil && im.Path != "" {
				uniq[im.Path] = im
			}
		}
		switch dt := a.Type.(type) {
		case expr.UserType:
			if loc := codegen.UserTypeLocation(dt); loc != nil && loc.RelImportPath != "" {
				imp := &codegen.ImportSpec{
					Name: loc.PackageName(),
					Path: JoinImportPath(genpkg, loc.RelImportPath),
				}
				uniq[imp.Path] = imp
			}
			visit(dt.Attribute())
		case *expr.Array:
			visit(dt.ElemType)
		case *expr.Map:
			visit(dt.KeyType)
			visit(dt.ElemType)
		case *expr.Object:
			for _, nat := range *dt {
				visit(nat.Attribute)
			}
		case expr.CompositeExpr:
			visit(dt.Attribute())
		}
	}
	visit(att)
	if len(uniq) == 0 {
		return nil
	}
	paths := make([]string, 0, len(uniq))
	for p := range uniq {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	imports := make([]*codegen.ImportSpec, 0, len(paths))
	for _, p := range paths {
		imports = append(imports, uniq[p])
	}
	return imports
}
