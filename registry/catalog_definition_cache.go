// Package registry reuses validation only for an identical saved definition and
// the same service or native-agent rules. Redis snapshots remain responsible for
// current JSON, admission state, and retirement membership on every read.
package registry

import (
	"crypto/sha256"
	"fmt"

	internaladmission "goa.design/goa-ai/internal/toolregistry/admission"
)

// catalogDefinitionDigest hashes every saved byte through a fixed scratch
// buffer. The buffer bounds temporary copying for one lookup; it does not limit
// the accepted definition size or retain any saved JSON after hashing.
func catalogDefinitionDigest(raw string) [sha256.Size]byte {
	hash := sha256.New()
	var scratch [32 * 1024]byte
	for len(raw) > 0 {
		count := copy(scratch[:], raw)
		// SHA-256 writes accept every byte and cannot fail.
		if _, err := hash.Write(scratch[:count]); err != nil {
			panic(err)
		}
		raw = raw[count:]
	}
	var digest [sha256.Size]byte
	hash.Sum(digest[:0])
	return digest
}

// validatedDefinition hashes the current saved JSON before considering reuse.
// A changed byte or validation mode requires strict decoding and validation.
// Only one cold definition is decoded at a time, so concurrent requests cannot
// create many large temporary parsing trees. Warm reads do not wait for it.
func (c *toolsetCatalog) validatedDefinition(name, raw string, nativeAgent bool) (*catalogDefinition, error) {
	digest := catalogDefinitionDigest(raw)
	if definition := c.cachedDefinition(name, digest, nativeAgent); definition != nil {
		return definition, nil
	}
	c.definitionLoadMu.Lock()
	defer c.definitionLoadMu.Unlock()
	if definition := c.cachedDefinition(name, digest, nativeAgent); definition != nil {
		return definition, nil
	}
	toolset, fingerprint, err := internaladmission.SavedToolsetFingerprint([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("decode definition %q: %w", name, err)
	}
	if toolset == nil || toolset.Name != name || toolset.RegisteredAt != "" {
		return nil, fmt.Errorf("toolset %q has invalid static definition", name)
	}
	if err := c.validator.ValidateToolSchemas(toolset.Tools); err != nil {
		return nil, fmt.Errorf("toolset %q invalid persisted schemas: %w", name, err)
	}
	if nativeAgent {
		if err := validateAgentTools(toolset.Tools); err != nil {
			return nil, err
		}
	}
	compiled, err := compileCatalogToolset(toolset, raw, fingerprint, c.validator)
	if err != nil {
		return nil, err
	}
	definition := compiled.catalogDefinition
	definition.digest = digest
	definition.nativeAgent = nativeAgent
	c.definitionsMu.Lock()
	c.definitions[name] = definition
	c.definitionsMu.Unlock()
	return definition, nil
}

// cachedDefinition returns validation only when every saved byte and the
// applicable validation rules match. Tokens and semantic fingerprints do not
// establish this match because they can remain equal after a replacement.
func (c *toolsetCatalog) cachedDefinition(name string, digest [sha256.Size]byte, nativeAgent bool) *catalogDefinition {
	c.definitionsMu.RLock()
	definition := c.definitions[name]
	c.definitionsMu.RUnlock()
	if definition != nil && definition.digest == digest && definition.nativeAgent == nativeAgent {
		return definition
	}
	return nil
}

// forgetDefinition drops retained metadata when a read observes a removed name.
// Other readers keep their independently selected validation results.
func (c *toolsetCatalog) forgetDefinition(name string) {
	c.definitionsMu.Lock()
	delete(c.definitions, name)
	c.definitionsMu.Unlock()
}

// pruneDefinitions drops cached names absent from a successful catalog listing.
// Concurrent additions may lose a cache entry and validate again; this never
// changes the Redis snapshot or authority selected by a caller.
func (c *toolsetCatalog) pruneDefinitions(keys []string) {
	present := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		present[key] = struct{}{}
	}
	c.definitionsMu.Lock()
	defer c.definitionsMu.Unlock()
	for name := range c.definitions {
		if _, exists := present[toolsetCatalogKey(name)]; !exists {
			delete(c.definitions, name)
		}
	}
}
