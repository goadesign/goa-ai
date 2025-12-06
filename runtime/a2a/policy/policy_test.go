package policy

import (
	"context"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// TestPolicyHeaderExtractionProperty verifies Property 8: Policy Header Extraction.
// **Feature: a2a-codegen-refactor, Property 8: Policy Header Extraction**
// *For any* valid header string, ExtractPolicyFromHeaders SHALL correctly parse skill lists.
// **Validates: Requirements 6.1, 6.2**
func TestPolicyHeaderExtractionProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("empty headers produce empty policy", prop.ForAll(
		func(_ int) bool {
			p := ExtractPolicyFromHeaders("", "")
			return len(p.AllowList) == 0 && len(p.DenyList) == 0
		},
		gen.Int(),
	))

	properties.Property("single skill is parsed correctly", prop.ForAll(
		func(skill string) bool {
			if skill == "" || strings.ContainsAny(skill, ", \t\n") {
				return true // Skip invalid inputs
			}
			p := ExtractPolicyFromHeaders(skill, "")
			return len(p.AllowList) == 1 && p.AllowList[0] == skill
		},
		gen.AlphaString(),
	))

	properties.Property("comma-separated skills are parsed", prop.ForAll(
		func(skills []string) bool {
			// Filter out empty strings
			validSkills := make([]string, 0, len(skills))
			for _, s := range skills {
				s = strings.TrimSpace(s)
				if s != "" && !strings.ContainsAny(s, ",") {
					validSkills = append(validSkills, s)
				}
			}
			if len(validSkills) == 0 {
				return true
			}

			header := strings.Join(validSkills, ",")
			p := ExtractPolicyFromHeaders(header, "")

			return len(p.AllowList) == len(validSkills)
		},
		gen.SliceOf(gen.AlphaString()),
	))

	properties.Property("whitespace is trimmed", prop.ForAll(
		func(skill string) bool {
			if skill == "" || strings.ContainsAny(skill, ", \t\n") {
				return true
			}
			header := "  " + skill + "  ,  " + skill + "  "
			p := ExtractPolicyFromHeaders(header, "")
			return len(p.AllowList) == 2 && p.AllowList[0] == skill && p.AllowList[1] == skill
		},
		gen.AlphaString(),
	))

	properties.Property("deny header is parsed separately", prop.ForAll(
		func(allow, deny string) bool {
			if allow == "" || deny == "" {
				return true
			}
			if strings.ContainsAny(allow, ", \t\n") || strings.ContainsAny(deny, ", \t\n") {
				return true
			}
			p := ExtractPolicyFromHeaders(allow, deny)
			return len(p.AllowList) == 1 && p.AllowList[0] == allow &&
				len(p.DenyList) == 1 && p.DenyList[0] == deny
		},
		gen.AlphaString(),
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// TestSkillFilteringProperty verifies Property 9: Skill Filtering Correctness.
// **Feature: a2a-codegen-refactor, Property 9: Skill Filtering Correctness**
// *For any* skill list and policy, FilterSkills SHALL correctly apply allow/deny rules.
// **Validates: Requirements 6.3**
func TestSkillFilteringProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("nil policy returns all skills", prop.ForAll(
		func(skills []string) bool {
			result := FilterSkills(skills, nil)
			return len(result) == len(skills)
		},
		gen.SliceOf(gen.AlphaString()),
	))

	properties.Property("empty policy returns all skills", prop.ForAll(
		func(skills []string) bool {
			p := &Policy{}
			result := FilterSkills(skills, p)
			return len(result) == len(skills)
		},
		gen.SliceOf(gen.AlphaString()),
	))

	properties.Property("deny list excludes skills", prop.ForAll(
		func(skills []string) bool {
			if len(skills) == 0 {
				return true
			}
			// Deny the first skill
			p := &Policy{DenyList: []string{skills[0]}}
			result := FilterSkills(skills, p)

			// Count occurrences of denied skill in original
			deniedCount := 0
			for _, s := range skills {
				if s == skills[0] {
					deniedCount++
				}
			}

			return len(result) == len(skills)-deniedCount
		},
		gen.SliceOfN(3, gen.AlphaString()),
	))

	properties.Property("allow list restricts to allowed skills", prop.ForAll(
		func(skills []string) bool {
			if len(skills) < 2 {
				return true
			}
			// Only allow the first skill
			p := &Policy{AllowList: []string{skills[0]}}
			result := FilterSkills(skills, p)

			// All results should be the allowed skill
			for _, s := range result {
				if s != skills[0] {
					return false
				}
			}
			return true
		},
		gen.SliceOfN(3, gen.AlphaString()),
	))

	properties.Property("deny takes precedence over allow", prop.ForAll(
		func(skill string) bool {
			if skill == "" {
				return true
			}
			// Both allow and deny the same skill
			p := &Policy{
				AllowList: []string{skill},
				DenyList:  []string{skill},
			}
			result := FilterSkills([]string{skill}, p)
			return len(result) == 0
		},
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// TestSkillAccessValidationProperty verifies Property 10: Skill Access Validation.
// **Feature: a2a-codegen-refactor, Property 10: Skill Access Validation**
// *For any* skill and policy, ValidateSkillAccess SHALL correctly determine access.
// **Validates: Requirements 6.4**
func TestSkillAccessValidationProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("nil policy allows all skills", prop.ForAll(
		func(skill string) bool {
			return ValidateSkillAccess(skill, nil)
		},
		gen.AlphaString(),
	))

	properties.Property("empty policy allows all skills", prop.ForAll(
		func(skill string) bool {
			p := &Policy{}
			return ValidateSkillAccess(skill, p)
		},
		gen.AlphaString(),
	))

	properties.Property("denied skill is not accessible", prop.ForAll(
		func(skill string) bool {
			if skill == "" {
				return true
			}
			p := &Policy{DenyList: []string{skill}}
			return !ValidateSkillAccess(skill, p)
		},
		gen.AlphaString(),
	))

	properties.Property("allowed skill is accessible", prop.ForAll(
		func(skill string) bool {
			if skill == "" {
				return true
			}
			p := &Policy{AllowList: []string{skill}}
			return ValidateSkillAccess(skill, p)
		},
		gen.AlphaString(),
	))

	properties.Property("skill not in allow list is not accessible", prop.ForAll(
		func(skill, other string) bool {
			if skill == "" || other == "" || skill == other {
				return true
			}
			p := &Policy{AllowList: []string{other}}
			return !ValidateSkillAccess(skill, p)
		},
		gen.AlphaString(),
		gen.AlphaString(),
	))

	properties.Property("deny takes precedence over allow", prop.ForAll(
		func(skill string) bool {
			if skill == "" {
				return true
			}
			p := &Policy{
				AllowList: []string{skill},
				DenyList:  []string{skill},
			}
			return !ValidateSkillAccess(skill, p)
		},
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// TestPolicyContextRoundTrip verifies policy context injection and retrieval.
func TestPolicyContextRoundTrip(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("policy survives context round-trip", prop.ForAll(
		func(allow, deny []string) bool {
			p := &Policy{AllowList: allow, DenyList: deny}
			ctx := InjectPolicyToContext(context.Background(), p)
			retrieved := PolicyFromContext(ctx)

			if retrieved == nil {
				return false
			}
			if len(retrieved.AllowList) != len(allow) {
				return false
			}
			if len(retrieved.DenyList) != len(deny) {
				return false
			}
			return true
		},
		gen.SliceOf(gen.AlphaString()),
		gen.SliceOf(gen.AlphaString()),
	))

	properties.Property("nil policy from empty context", prop.ForAll(
		func(_ int) bool {
			p := PolicyFromContext(context.Background())
			return p == nil
		},
		gen.Int(),
	))

	properties.TestingRun(t)
}
