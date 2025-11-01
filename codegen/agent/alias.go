package codegen

import (
	goaexpr "goa.design/goa/v3/expr"
)

// ToolAttrAliasesMethod reports whether the tool attribute defines a user type
// whose name matches the method's user type name or any of its Extend base user
// type names. It returns false if either attribute is nil or does not describe
// a user type.
func ToolAttrAliasesMethod(toolAttr, methodAttr *goaexpr.AttributeExpr) bool {
	if toolAttr == nil || methodAttr == nil {
		return false
	}
	ut, ok := toolAttr.Type.(goaexpr.UserType)
	if !ok || ut == nil {
		return false
	}
	toolName := ut.Name()
	if toolName == "" {
		return false
	}
	return FindMatchingBaseUserType(methodAttr, toolName) != nil
}

// FindMatchingBaseUserType returns the user type whose name matches the given
// name among the method attribute user type and any of its recursively extended
// bases. Returns nil if no match is found.
func FindMatchingBaseUserType(methodAttr *goaexpr.AttributeExpr, name string) goaexpr.UserType {
	if methodAttr == nil || name == "" {
		return nil
	}
	var match goaexpr.UserType
	visited := make(map[string]struct{})
	var walk func(a *goaexpr.AttributeExpr)
	walk = func(a *goaexpr.AttributeExpr) {
		if a == nil || a.Type == nil || a.Type == goaexpr.Empty {
			return
		}
		if ut, ok := a.Type.(goaexpr.UserType); ok {
			if ut.Name() == name {
				match = ut
				return
			}
			if _, ok := visited[ut.ID()]; !ok {
				visited[ut.ID()] = struct{}{}
				walk(ut.Attribute())
			}
		}
		for _, bt := range a.Bases {
			if btt, ok := bt.(goaexpr.UserType); ok {
				if btt.Name() == name {
					match = btt
					return
				}
				walk(btt.Attribute())
				if match != nil {
					return
				}
			}
		}
	}
	walk(methodAttr)
	return match
}
