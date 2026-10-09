// Package codegen turns Goa and Goa-AI designs into generated agent code.
//
// This file defines stable ordering when generated declarations request the
// same Go name.
package codegen

import (
	"strings"

	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

// ComparePackageName orders two generated tool specification names.
func (o specNameOrder) ComparePackageName(other goacodegen.PackageNameOrder) int {
	right := other.(specNameOrder)
	if compared := strings.Compare(o.packagePath, right.packagePath); compared != 0 {
		return compared
	}
	return strings.Compare(o.key, right.key)
}

// ComparePackageName orders conversion helpers by their owning transform and
// authored location.
func (o transformHelperNameOrder) ComparePackageName(other goacodegen.PackageNameOrder) int {
	right := other.(transformHelperNameOrder)
	if compared := strings.Compare(o.packagePath, right.packagePath); compared != 0 {
		return compared
	}
	if compared := strings.Compare(o.key, right.key); compared != 0 {
		return compared
	}
	return o.location.Compare(right.location)
}

// ComparePackageName preserves ordinary names, then orders nested types by
// their source package, name, and ID.
func (o localizedTypeNameOrder) ComparePackageName(other goacodegen.PackageNameOrder) int {
	right := other.(localizedTypeNameOrder)
	// Existing model declarations keep their names when a native transport
	// requests a name already used by another authored type.
	for _, compared := range []int{
		int(o.jsonContract) - int(right.jsonContract),
		strings.Compare(o.packagePath, right.packagePath),
		strings.Compare(o.sourcePath, right.sourcePath),
		strings.Compare(o.sourceName, right.sourceName),
		strings.Compare(o.sourceID, right.sourceID),
		int(o.role) - int(right.role),
	} {
		if compared != 0 {
			return compared
		}
	}
	return 0
}

// newLocalizedTypeNameOrder copies the stable source type details used while
// Goa assigns generated package names.
func newLocalizedTypeNameOrder(packagePath string, source goaexpr.UserType, role localizedTypeNameRole, contract specJSONContract) localizedTypeNameOrder {
	var sourcePath string
	if location := goacodegen.UserTypeLocation(source); location != nil {
		sourcePath = location.RelImportPath
	}
	sourceID := source.ID()
	if result, ok := source.(*goaexpr.ResultTypeExpr); ok {
		sourceID = result.Identifier
	}
	return localizedTypeNameOrder{
		jsonContract: contract,
		packagePath:  packagePath,
		sourcePath:   sourcePath,
		sourceName:   source.Name(),
		sourceID:     sourceID,
		role:         role,
	}
}
