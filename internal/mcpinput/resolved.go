// Package mcpinput reads complete inherited contracts before Goa's finalization phase.
// A detached graph protects authored declarations; Goa owns all field merges and
// inherited constraints. Generators still receive the exact original attributes.
package mcpinput

import "goa.design/goa/v3/expr"

type (
	// contractGraph retains the relationship between copied and authored fields.
	// Ordinary contracts need no copy and keep their existing attribute identity.
	contractGraph struct {
		copier *expr.AttributeGraphCopier
	}
)

// Resolved returns the complete fields and constraints of an attribute without
// changing its authored graph. Contracts without inheritance remain unchanged.
func Resolved(attribute *expr.AttributeExpr) *expr.AttributeExpr {
	return newContractGraph(attribute).attribute(attribute)
}

// newContractGraph copies related roots together so shared and recursive types
// remain shared. Inheritance sources are finalized before their consumers; Goa
// then merges each copied root using its ordinary finalization rules.
func newContractGraph(roots ...*expr.AttributeExpr) contractGraph {
	var ordered []*expr.AttributeExpr
	seen := make(map[*expr.AttributeExpr]bool)
	for _, root := range roots {
		collectContractAttributes(root, seen, &ordered)
	}
	var graph contractGraph
	for _, attribute := range ordered {
		if len(attribute.Bases) > 0 || len(attribute.References) > 0 {
			graph.copier = expr.NewAttributeGraphCopier()
			break
		}
	}
	if graph.copier == nil {
		return graph
	}
	for _, root := range roots {
		graph.copier.Copy(root)
	}
	for _, attribute := range ordered {
		copied := graph.copier.Copy(attribute)
		for _, sources := range [][]expr.DataType{copied.References, copied.Bases} {
			for _, source := range sources {
				if named, ok := source.(expr.UserType); ok {
					named.Finalize()
				}
			}
		}
	}
	for _, root := range roots {
		if root != nil {
			graph.copier.Copy(root).Finalize()
		}
	}
	return graph
}

// collectContractAttributes orders inheritance sources before consumers and
// visits each concrete declaration once. Unlike a type-name walk, it also keeps
// distinct copies of one named type and follows base and reference declarations.
func collectContractAttributes(attribute *expr.AttributeExpr, seen map[*expr.AttributeExpr]bool, ordered *[]*expr.AttributeExpr) {
	if attribute == nil || seen[attribute] {
		return
	}
	seen[attribute] = true
	for _, sources := range [][]expr.DataType{attribute.References, attribute.Bases} {
		for _, source := range sources {
			if named, ok := source.(expr.UserType); ok {
				collectContractAttributes(named.Attribute(), seen, ordered)
			}
		}
	}
	switch actual := attribute.Type.(type) {
	case expr.UserType:
		collectContractAttributes(actual.Attribute(), seen, ordered)
	case *expr.Object:
		for _, field := range *actual {
			collectContractAttributes(field.Attribute, seen, ordered)
		}
	case *expr.Union:
		for _, branch := range actual.Values {
			collectContractAttributes(branch.Attribute, seen, ordered)
		}
	case *expr.Array:
		collectContractAttributes(actual.ElemType, seen, ordered)
	case *expr.Map:
		collectContractAttributes(actual.KeyType, seen, ordered)
		collectContractAttributes(actual.ElemType, seen, ordered)
	}
	*ordered = append(*ordered, attribute)
}

// attribute returns the resolved copy selected for this graph.
func (g contractGraph) attribute(attribute *expr.AttributeExpr) *expr.AttributeExpr {
	if g.copier == nil {
		return attribute
	}
	return g.copier.Copy(attribute)
}

// original returns the authored declaration selected by the resolved copy.
func (g contractGraph) original(attribute *expr.AttributeExpr) *expr.AttributeExpr {
	if g.copier == nil {
		return attribute
	}
	return g.copier.Original(attribute)
}
