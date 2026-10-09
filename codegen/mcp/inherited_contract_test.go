// These peers keep the ordinary generated HTTP path while deriving contract
// fields from shared Goa types. Native endpoints, selected views and host input
// must behave exactly as they do when every field is written directly.
package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

type (
	// inheritanceEdit changes one authored declaration in a synthetic design.
	inheritanceEdit struct {
		start, end int
		text       string
	}
)

func TestMCPGeneratedInheritedInputExchange(t *testing.T) {
	design := inheritedPeerTypes(t, inputExchangeDesign, "content", "continuation", "pending", "operation", "promptMessage", "promptComplete", "promptOperation")
	runMCPPeer(t, "input-peer.local", design, inputExchangeRuntime)
}

func TestMCPGeneratedInheritedTaskLifecycle(t *testing.T) {
	design := inheritedPeerTypes(t, taskOwnerDesign, "content", "question", "pending", "metadata", "failure", "observation")
	runMCPPeer(t, "task-peer.local", design, taskOwnerRuntime)
}

func TestMCPGeneratedInheritedCatalogs(t *testing.T) {
	design := inheritedPeerTypes(t, catalogPeerDesign, "tools", "prompts", "selections", "event")
	runMCPPeer(t, "catalog-peer.local", design, catalogPeerRuntime)
}

// inheritedPeerTypes moves selected field declarations into a base type and
// retains each original type name as an Extend declaration. Existing service
// implementations therefore exercise inheritance without changing their calls.
func inheritedPeerTypes(t *testing.T, source string, names ...string) string {
	t.Helper()
	positions := token.NewFileSet()
	file, err := parser.ParseFile(positions, "design.go", source, 0)
	require.NoError(t, err)
	var edits []inheritanceEdit
	for _, declaration := range file.Decls {
		group, ok := declaration.(*ast.GenDecl)
		if !ok || group.Tok != token.VAR {
			continue
		}
		for _, spec := range group.Specs {
			value := spec.(*ast.ValueSpec)
			if len(value.Names) != 1 || !slices.Contains(names, value.Names[0].Name) {
				continue
			}
			call, ok := value.Values[0].(*ast.CallExpr)
			require.True(t, ok)
			literal, ok := call.Args[0].(*ast.BasicLit)
			require.True(t, ok)
			typeName, err := strconv.Unquote(literal.Value)
			require.NoError(t, err)
			variable := value.Names[0]
			edits = append(edits,
				inheritanceEdit{start: positions.Position(variable.End()).Offset, end: positions.Position(variable.End()).Offset, text: "Fields"},
				inheritanceEdit{start: positions.Position(literal.Pos()).Offset, end: positions.Position(literal.End()).Offset, text: strconv.Quote(typeName + "Fields")},
				inheritanceEdit{start: positions.Position(group.End()).Offset, end: positions.Position(group.End()).Offset, text: "\nvar " + variable.Name + "=Type(" + strconv.Quote(typeName) + ",func(){Extend(" + variable.Name + "Fields)})\n"},
			)
		}
	}
	require.Len(t, edits, len(names)*3)
	slices.SortFunc(edits, func(a, b inheritanceEdit) int { return b.start - a.start })
	for _, edit := range edits {
		source = source[:edit.start] + edit.text + source[edit.end:]
	}
	return source
}
