package a2a

import (
	"context"
	"fmt"
	"sort"

	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/registry"
)

// RegistrationError describes a failure to register an A2A provider discovered
// via a registry. It records the A2A suite, provider URL (when known), and the
// underlying error to aid debugging and observability.
type RegistrationError struct {
	// Suite is the A2A suite identifier being registered (for example,
	// "service.agent.toolset").
	Suite string
	// URL is the provider base URL as advertised by the registry, when available.
	URL string
	// Err is the underlying cause of the registration failure.
	Err error
}

// RegisterProviderFromCard registers an A2A provider discovered via a registry
// AgentCard using RegisterProvider. It first validates consistency between the
// AgentCard and the ProviderConfig and wraps any failure in a RegistrationError
// that records suite and URL.
func RegisterProviderFromCard(
	ctx context.Context,
	rt *agentruntime.Runtime,
	caller Caller,
	cfg ProviderConfig,
	card *registry.AgentCard,
) error {
	if err := ValidateAgentCardConsistency(card, cfg); err != nil {
		return err
	}
	if err := RegisterProvider(ctx, rt, caller, cfg); err != nil {
		return &RegistrationError{
			Suite: cfg.Suite,
			URL:   card.URL,
			Err:   err,
		}
	}
	return nil
}

// IsA2AEntry reports whether the given registry search result represents an A2A
// provider entry. Registries advertise A2A providers using Type "a2a".
func IsA2AEntry(res *registry.SearchResult) bool {
	if res == nil {
		return false
	}
	return res.Type == "a2a"
}

// ValidateAgentCardConsistency compares a registry AgentCard with a ProviderConfig
// generated from design. It returns nil when all skills match by ID and, when
// present, description. Otherwise it returns a RegistrationError describing the
// mismatch so callers can decide whether to trust the registry entry.
func ValidateAgentCardConsistency(card *registry.AgentCard, cfg ProviderConfig) error {
	if card == nil {
		return &RegistrationError{
			Suite: cfg.Suite,
			Err:   fmt.Errorf("agent card is nil"),
		}
	}

	skillsByID := make(map[string]SkillConfig, len(cfg.Skills))
	for _, sk := range cfg.Skills {
		skillsByID[sk.ID] = sk
	}

	for _, s := range card.Skills {
		cfgSkill, ok := skillsByID[s.ID]
		if !ok {
			return &RegistrationError{
				Suite: cfg.Suite,
				URL:   card.URL,
				Err:   fmt.Errorf("unexpected skill %q in agent card", s.ID),
			}
		}
		if cfgSkill.Description != "" && s.Description != "" && cfgSkill.Description != s.Description {
			return &RegistrationError{
				Suite: cfg.Suite,
				URL:   card.URL,
				Err:   fmt.Errorf("description mismatch for skill %q: config=%q card=%q", s.ID, cfgSkill.Description, s.Description),
			}
		}
		delete(skillsByID, s.ID)
	}

	if len(skillsByID) > 0 {
		missing := make([]string, 0, len(skillsByID))
		for id := range skillsByID {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return &RegistrationError{
			Suite: cfg.Suite,
			URL:   card.URL,
			Err:   fmt.Errorf("agent card missing skills: %v", missing),
		}
	}

	return nil
}

// Error implements the error interface.
func (e *RegistrationError) Error() string {
	if e == nil {
		return ""
	}
	if e.URL == "" {
		return fmt.Sprintf("A2A provider registration for suite %q failed: %v", e.Suite, e.Err)
	}
	return fmt.Sprintf("A2A provider registration for suite %q at %q failed: %v", e.Suite, e.URL, e.Err)
}

// Unwrap returns the underlying error.
func (e *RegistrationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
