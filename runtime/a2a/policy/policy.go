// Package policy provides skill filtering and access control for A2A agents.
// It supports policy injection via HTTP headers and context-based access validation.
package policy

import (
	"context"
	"strings"
)

// contextKey is the type for context keys in this package.
type contextKey int

// Header constants for policy injection.
const (
	// AllowSkillsHeader specifies skills to allow (comma-separated).
	AllowSkillsHeader = "X-A2A-Allow-Skills"
	// DenySkillsHeader specifies skills to deny (comma-separated).
	DenySkillsHeader = "X-A2A-Deny-Skills"
)

const (
	policyKey contextKey = iota + 1
)

// Policy represents skill access control rules.
type Policy struct {
	// AllowList contains skills explicitly allowed. Empty means all allowed.
	AllowList []string
	// DenyList contains skills explicitly denied.
	DenyList []string
}

// ExtractPolicyFromHeaders parses policy headers and returns a Policy.
// Headers are expected to contain comma-separated skill names.
func ExtractPolicyFromHeaders(allowHeader, denyHeader string) *Policy {
	return &Policy{
		AllowList: parseSkillList(allowHeader),
		DenyList:  parseSkillList(denyHeader),
	}
}

// parseSkillList parses a comma-separated list of skill names.
func parseSkillList(header string) []string {
	if header == "" {
		return nil
	}
	parts := strings.Split(header, ",")
	skills := make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s != "" {
			skills = append(skills, s)
		}
	}
	return skills
}

// InjectPolicyToContext adds the policy to the context.
func InjectPolicyToContext(ctx context.Context, p *Policy) context.Context {
	return context.WithValue(ctx, policyKey, p)
}

// PolicyFromContext retrieves the policy from context.
// Returns nil if no policy is set.
func PolicyFromContext(ctx context.Context) *Policy {
	p, _ := ctx.Value(policyKey).(*Policy)
	return p
}

// FilterSkills applies the policy to a list of skills and returns allowed skills.
// If AllowList is non-empty, only skills in the allow list are included.
// Skills in DenyList are always excluded.
func FilterSkills(skills []string, p *Policy) []string {
	if p == nil {
		return skills
	}

	// Build lookup sets for O(1) checks
	allowSet := make(map[string]struct{}, len(p.AllowList))
	for _, s := range p.AllowList {
		allowSet[s] = struct{}{}
	}
	denySet := make(map[string]struct{}, len(p.DenyList))
	for _, s := range p.DenyList {
		denySet[s] = struct{}{}
	}

	result := make([]string, 0, len(skills))
	for _, skill := range skills {
		// Check deny list first (deny takes precedence)
		if _, denied := denySet[skill]; denied {
			continue
		}
		// If allow list is specified, skill must be in it
		if len(allowSet) > 0 {
			if _, allowed := allowSet[skill]; !allowed {
				continue
			}
		}
		result = append(result, skill)
	}
	return result
}

// ValidateSkillAccess checks if a skill is allowed by the policy.
// Returns true if the skill is accessible, false otherwise.
func ValidateSkillAccess(skill string, p *Policy) bool {
	if p == nil {
		return true
	}

	// Check deny list first
	for _, s := range p.DenyList {
		if s == skill {
			return false
		}
	}

	// If allow list is empty, all non-denied skills are allowed
	if len(p.AllowList) == 0 {
		return true
	}

	// Check allow list
	for _, s := range p.AllowList {
		if s == skill {
			return true
		}
	}

	return false
}
