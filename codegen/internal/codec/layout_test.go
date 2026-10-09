// These unit fixtures provide the service layout that protocol codecs receive
// from Goa. End-to-end placement tests use real evaluated service plans.
package codec

import (
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

// addCodecTestValue supplies layout imports for the existing hand-built codec
// fixtures. A separate declaration catalog avoids adding synthetic service
// names to the codec generation being tested.
func addCodecTestValue(t *testing.T, plan *Plan, key, name string, attribute *expr.AttributeExpr, direction Direction) (*Value, error) {
	t.Helper()
	generation, err := codegen.NewGeneration("example.com/gen", nil)
	require.NoError(t, err)
	preferences := map[string]string{"example.com/gen/widgets": "widgets"}
	layout, err := codegen.PlanGoType(attribute, codegen.GoTypePlanOptions{
		Owner:            "example.com/gen/widgets",
		Policy:           codegen.GoLayoutPolicy{UseDefault: true, SumType: true},
		RetainNamedValue: true,
		Bind: func(request codegen.GoTypeBindingRequest) (codegen.GoTypeBinding, error) {
			owner := request.InheritedOwner
			if location := codegen.UserTypeLocation(request.Attribute.Type); location != nil {
				owner = path.Join(generation.GenPkg(), location.RelImportPath)
				preferences[owner] = strings.ToLower(location.PackageName())
			}
			pkg, err := generation.ClaimPackage(owner)
			if err != nil {
				return codegen.GoTypeBinding{}, err
			}
			binding := codegen.GoTypeBinding{Owner: owner, PreferredImportName: preferences[owner]}
			if request.Kind == codegen.GoUnion {
				binding.Union, err = pkg.DeclareUnion(request.Attribute)
			} else {
				binding.Type, err = pkg.DeclareUserType(request.Attribute.Type.(expr.UserType))
			}
			return binding, err
		},
	})
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())
	return plan.Add(key, name, attribute, layout, direction)
}
