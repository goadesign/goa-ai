package a2a

import (
	"context"
	"errors"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/stretchr/testify/require"

	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/registry"
)

// TestIsA2AEntryProperty verifies Property 16: Registry A2A Detection.
// **Feature: a2a-architecture-redesign, Property 16: Registry A2A Detection**
// *For any* registry search result with Type "a2a", IsA2AEntry MUST return true
// and MUST return false for any other type or nil.
// **Validates: Requirements 8.1**
func TestIsA2AEntryProperty(t *testing.T) {
	t.Helper()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 50
	properties := gopter.NewProperties(parameters)

	properties.Property("detects only a2a entries", prop.ForAll(
		func(resultType string) bool {
			if resultType == "" {
				resultType = "toolset"
			}
			res := &registry.SearchResult{Type: resultType}
			if resultType == "a2a" {
				return IsA2AEntry(res)
			}
			return !IsA2AEntry(res)
		},
		gen.AlphaString(),
	))

	properties.TestingRun(t)

	// Explicit nil check
	require.False(t, IsA2AEntry(nil))
}

// TestValidateAgentCardConsistencyProperty verifies Property 17:
// AgentCard Consistency with ProviderConfig.
// **Feature: a2a-architecture-redesign, Property 17: AgentCard Consistency with ProviderConfig**
// *For any* AgentCard and ProviderConfig pair, validation should succeed when
// skills match by ID and description, and should return a RegistrationError
// otherwise.
// **Validates: Requirements 8.3**
func TestValidateAgentCardConsistencyProperty(t *testing.T) {
	t.Helper()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 50
	properties := gopter.NewProperties(parameters)

	properties.Property("matching skills succeed, mismatches fail", prop.ForAll(
		func(ids []string) bool {
			if len(ids) == 0 {
				return true
			}
			unique := dedupeNonEmpty(ids)
			if len(unique) == 0 {
				return true
			}

			cfg := ProviderConfig{
				Suite: "svc.agent.tools",
				Skills: make([]SkillConfig, 0, len(unique)),
			}
			card := &registry.AgentCard{
				URL:    "https://provider.example.com",
				Skills: make([]*registry.Skill, 0, len(unique)),
			}

			for _, id := range unique {
				desc := "description for " + id
				cfg.Skills = append(cfg.Skills, SkillConfig{
					ID:          id,
					Description: desc,
					Payload: tools.TypeSpec{
						Name:   "Payload",
						Schema: []byte(`{"type":"object"}`),
					},
				})
				card.Skills = append(card.Skills, &registry.Skill{
					ID:          id,
					Description: desc,
				})
			}

			// Matching case: must succeed.
			if err := ValidateAgentCardConsistency(card, cfg); err != nil {
				return false
			}

			// Introduce mismatch by dropping one skill from the card.
			card.Skills = card.Skills[:len(card.Skills)-1]
			err := ValidateAgentCardConsistency(card, cfg)
			var regErr *RegistrationError
			if !errors.As(err, &regErr) {
				return false
			}
			if regErr.Suite != cfg.Suite {
				return false
			}
			if regErr.URL != card.URL {
				return false
			}
			return regErr.Err != nil
		},
		gen.SliceOf(gen.AlphaString()),
	))

	properties.TestingRun(t)
}

// TestRegistrationErrorStructure verifies Property 18: Registration Error Structure.
// **Feature: a2a-architecture-redesign, Property 18: Registration Error Structure**
// *For any* failed A2A registration, the error should contain the suite, URL,
// and underlying error.
// **Validates: Requirements 8.4**
func TestRegistrationErrorStructure(t *testing.T) {
	t.Helper()

	baseErr := errors.New("registration failed")
	err := &RegistrationError{
		Suite: "svc.agent.tools",
		URL:   "https://provider.example.com",
		Err:   baseErr,
	}

	require.Equal(t, "svc.agent.tools", err.Suite)
	require.Equal(t, "https://provider.example.com", err.URL)
	require.ErrorContains(t, err, "registration failed")
	require.Equal(t, baseErr, errors.Unwrap(err))
}

// TestRegisterProviderFromCard verifies that RegisterProviderFromCard delegates
// to RegisterProvider and wraps failures in RegistrationError.
// **Feature: a2a-architecture-redesign**
// **Validates: Requirements 8.1, 8.4**
func TestRegisterProviderFromCard(t *testing.T) {
	t.Helper()

	rt := agentruntime.New()
	cfg := ProviderConfig{
		Suite: "svc.agent.tools",
		Skills: []SkillConfig{
			{
				ID:          "tools.echo",
				Description: "echo",
				Payload: tools.TypeSpec{
					Name:   "Payload",
					Schema: []byte(`{"type":"object"}`),
				},
				Result: tools.TypeSpec{
					Name: "Result",
				},
			},
		},
	}
	card := &registry.AgentCard{
		URL: "https://provider.example.com",
		Skills: []*registry.Skill{
			{
				ID:          "tools.echo",
				Description: "echo",
			},
		},
	}

	// Success path: validation and registration should succeed.
	caller := &recordingCaller{}
	err := RegisterProviderFromCard(context.Background(), rt, caller, cfg, card)
	require.NoError(t, err)

	// Failure path: pass nil runtime to force RegisterProvider error.
	err = RegisterProviderFromCard(context.Background(), nil, caller, cfg, card)
	var regErr *RegistrationError
	require.Error(t, err)
	require.True(t, errors.As(err, &regErr))
	require.Equal(t, cfg.Suite, regErr.Suite)
	require.Equal(t, card.URL, regErr.URL)
}

// dedupeNonEmpty returns a slice with duplicates and empty strings removed.
func dedupeNonEmpty(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}


