// Package runtime reads selected registered sources from one planning activity's catalog.
// Applications use these copies to install typed inputs without reading the
// registry again or changing the selection retained for tool execution.
package runtime

import (
	"fmt"
	"maps"
	"slices"

	genregistry "goa.design/goa-ai/registry/gen/registry"
)

// ForEachRegisteredSource calls visit once for each distinct registry and exact
// registered source selected in this activity, ordered by the first derived tool name.
// Each source is an independent copy. Changing it cannot change a saved tool
// selection. A callback error stops the traversal and is returned to the caller.
// Static tools and direct registrations have no registered identity and are skipped.
func (c *RegistryCatalog) ForEachRegisteredSource(visit func(string, *genregistry.CatalogSource) error) error {
	if visit == nil {
		return fmt.Errorf("registered source visitor is required")
	}
	type (
		sourceKey      struct{ registry, encoded string }
		selectedSource struct {
			registry string
			source   *genregistry.CatalogSource
		}
	)
	seen := make(map[sourceKey]struct{})
	selected := make([]selectedSource, 0)
	for _, name := range slices.Sorted(maps.Keys(c.selections)) {
		selection := c.selections[name]
		if selection.resolution.Source == nil {
			continue
		}
		encoded, err := genregistry.EncodeCatalogSource(selection.resolution.Source)
		if err != nil {
			return fmt.Errorf("registered source for %q: %w", name, err)
		}
		key := sourceKey{registry: selection.registry, encoded: string(encoded)}
		if _, exists := seen[key]; exists {
			continue
		}
		source, err := genregistry.DecodeCatalogSource(encoded)
		if err != nil {
			return fmt.Errorf("copy registered source for %q: %w", name, err)
		}
		seen[key] = struct{}{}
		selected = append(selected, selectedSource{registry: selection.registry, source: source})
	}
	for _, selection := range selected {
		if err := visit(selection.registry, selection.source); err != nil {
			return err
		}
	}
	return nil
}
