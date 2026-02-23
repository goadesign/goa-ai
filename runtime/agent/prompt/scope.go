// Package prompt contains runtime prompt registry and override resolution logic.
package prompt

// ScopeMatches reports whether overrideScope applies to requestedScope.
//
// Contract:
//   - SessionID must match exactly when overrideScope.SessionID is set.
//   - Every label in overrideScope.Labels must exist with the same value in
//     requestedScope.Labels.
func ScopeMatches(overrideScope Scope, requestedScope Scope) bool {
	if overrideScope.SessionID != "" && overrideScope.SessionID != requestedScope.SessionID {
		return false
	}
	for key, value := range overrideScope.Labels {
		if requestedScope.Labels[key] != value {
			return false
		}
	}
	return true
}

// ScopePrecedence returns override precedence for conflict resolution.
//
// Higher values are more specific:
//   - Session-scoped overrides outrank non-session overrides.
//   - For the same session dimension, more constrained label sets outrank
//     less constrained ones.
func ScopePrecedence(scope Scope) int {
	precedence := len(scope.Labels)
	if scope.SessionID != "" {
		// Session is the strongest runtime-managed scope dimension.
		precedence += 1000
	}
	return precedence
}
