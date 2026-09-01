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

// ComparePackageName orders unknown-branch error functions by package and the
// exact union name that owns each function.
func (o unionErrorNameOrder) ComparePackageName(other goacodegen.PackageNameOrder) int {
	right := other.(unionErrorNameOrder)
	if compared := strings.Compare(o.packagePath, right.packagePath); compared != 0 {
		return compared
	}
	return strings.Compare(o.unionName, right.unionName)
}

// ComparePackageName orders generated nested types by their source package,
// name, and ID.
func (o localizedTypeNameOrder) ComparePackageName(other goacodegen.PackageNameOrder) int {
	right := other.(localizedTypeNameOrder)
	for _, compared := range []int{
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
func newLocalizedTypeNameOrder(packagePath string, source goaexpr.UserType, role localizedTypeNameRole) localizedTypeNameOrder {
	var sourcePath string
	if location := goacodegen.UserTypeLocation(source); location != nil {
		sourcePath = location.RelImportPath
	}
	return localizedTypeNameOrder{
		packagePath: packagePath,
		sourcePath:  sourcePath,
		sourceName:  source.Name(),
		sourceID:    source.ID(),
		role:        role,
	}
}
