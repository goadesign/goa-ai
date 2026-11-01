// Package prompt contains runtime prompt registry and override resolution logic.
package prompt

import (
	"sort"
	"strings"
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

// CanonicalLabels returns a deterministic string representation of scope labels,
// formatted as sorted comma-separated "key=value" pairs.
func CanonicalLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(labels))
	for k, v := range labels {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

// OverridePrecedes reports whether candidate outranks current when resolving overrides.
//
// Precedence order:
//  1. Higher ScopePrecedence (session presence, then label count)
//  2. Newer CreatedAt timestamp
//  3. Deterministic tie-breaker on equal CreatedAt: lexicographical comparison of
//     canonical sorted "k=v" label strings.
func OverridePrecedes(candidate, current *Override) bool {
	if current == nil {
		return true
	}
	if candidate == nil {
		return false
	}
	candLevel := ScopePrecedence(candidate.Scope)
	currLevel := ScopePrecedence(current.Scope)
	if candLevel != currLevel {
		return candLevel > currLevel
	}
	if !candidate.CreatedAt.Equal(current.CreatedAt) {
		return candidate.CreatedAt.After(current.CreatedAt)
	}
	return CanonicalLabels(candidate.Scope.Labels) > CanonicalLabels(current.Scope.Labels)
}
