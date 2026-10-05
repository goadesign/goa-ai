// Package mcp shares the requested and accepted subscription filters between
// HTTP producers and HTTP/stdio receivers. It rejects invalid event ordering
// and unrequested kinds without deciding resource ownership or authorization.
package mcp

import (
	"errors"
	"fmt"
	"slices"
)

type (
	subscriptionState struct {
		requested    SubscriptionFilter
		accepted     SubscriptionFilter
		acknowledged bool
	}
)

// acknowledge narrows the requested filter once. It copies the accepted values
// so later source or callback changes cannot alter this request's checks.
func (s *subscriptionState) acknowledge(accepted SubscriptionFilter) error {
	if s.acknowledged {
		return errors.New("subscription was acknowledged more than once")
	}
	if accepted.ToolsListChanged && !s.requested.ToolsListChanged || accepted.PromptsListChanged && !s.requested.PromptsListChanged || accepted.ResourcesListChanged && !s.requested.ResourcesListChanged {
		return errors.New("acknowledgment includes an unrequested notification kind")
	}
	for _, uri := range accepted.ResourceSubscriptions {
		if !slices.Contains(s.requested.ResourceSubscriptions, uri) {
			return fmt.Errorf("acknowledgment includes unrequested resource %q", uri)
		}
	}
	s.acknowledged, s.accepted = true, cloneSubscriptionFilter(accepted)
	return nil
}

// change checks ordering and the accepted notification kind. An updated address
// may identify a sub-resource, whose relationship and access the source owns.
func (s *subscriptionState) change(kind SubscriptionEventKind, uri string) error {
	if !s.acknowledged {
		return errors.New("subscription change arrived before acknowledgment")
	}
	var allowed bool
	switch kind {
	case SubscriptionAcknowledged:
		return errors.New("subscription acknowledgment is not a change")
	case SubscriptionToolsChanged:
		allowed = s.accepted.ToolsListChanged
	case SubscriptionPromptsChanged:
		allowed = s.accepted.PromptsListChanged
	case SubscriptionResourcesChanged:
		allowed = s.accepted.ResourcesListChanged
	case SubscriptionResourceUpdated:
		allowed = len(s.accepted.ResourceSubscriptions) > 0
		if err := validateContentURI(uri); err != nil {
			return err
		}
	}
	if !allowed {
		return fmt.Errorf("notification %q was not acknowledged", kind)
	}
	return nil
}
