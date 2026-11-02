// Package prompt contains runtime prompt registry and override resolution logic.
package prompt

import (
	"cmp"
	"maps"
	"slices"
)

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
		got, ok := requestedScope.Labels[key]
		if !ok || got != value {
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

// OverridePrecedes reports whether candidate outranks current when resolving overrides.
//
// Precedence order:
//  1. Session-scoped overrides outrank non-session overrides.
//  2. More constrained label sets outrank less constrained ones (higher label count).
//  3. Newer CreatedAt timestamp.
//  4. Deterministic tie-breaker on equal CreatedAt: lexicographical comparison of
//     sorted label key and value pairs.
func OverridePrecedes(candidate, current *Override) bool {
	if current == nil {
		return true
	}
	if candidate == nil {
		return false
	}
	candSession := candidate.Scope.SessionID != ""
	currSession := current.Scope.SessionID != ""
	if candSession != currSession {
		return candSession
	}
	if len(candidate.Scope.Labels) != len(current.Scope.Labels) {
		return len(candidate.Scope.Labels) > len(current.Scope.Labels)
	}
	if !candidate.CreatedAt.Equal(current.CreatedAt) {
		return candidate.CreatedAt.After(current.CreatedAt)
	}
	return compareLabelMaps(candidate.Scope.Labels, current.Scope.Labels) > 0
}

// compareLabelMaps compares two label maps by sorted key and value pairs.
func compareLabelMaps(left, right map[string]string) int {
	leftKeys := slices.Sorted(maps.Keys(left))
	rightKeys := slices.Sorted(maps.Keys(right))
	for index := 0; index < min(len(leftKeys), len(rightKeys)); index++ {
		leftKey := leftKeys[index]
		rightKey := rightKeys[index]
		if compared := cmp.Compare(leftKey, rightKey); compared != 0 {
			return compared
		}
		if compared := cmp.Compare(left[leftKey], right[rightKey]); compared != 0 {
			return compared
		}
	}
	return len(leftKeys) - len(rightKeys)
}
