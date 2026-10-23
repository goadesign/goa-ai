// Package registry reuses validation only for an identical saved definition and
// the same service or native-agent rules. Redis snapshots remain responsible for
// current JSON, admission state, and retirement membership on every read.
package registry

import (
	"context"
	"crypto/sha256"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

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
// Canceled waiting reads return immediately.
func (c *toolsetCatalog) validatedDefinition(ctx context.Context, name, raw string, nativeAgent bool) (_ *catalogDefinition, err error) {
	ctx, span := otel.Tracer("goa.design/goa-ai/registry").Start(ctx, "toolregistry.catalog.definition.validate",
		trace.WithAttributes(
			attribute.String("toolregistry.toolset", name),
			attribute.Bool("toolregistry.catalog.definition_cache_hit", false),
		))
	defer finishCatalogSpan(ctx, span, &err)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	digest := catalogDefinitionDigest(raw)
	if definition := c.cachedDefinition(name, digest, nativeAgent); definition != nil {
		span.SetAttributes(attribute.Bool("toolregistry.catalog.definition_cache_hit", true))
		return definition, nil
	}
	select {
	case c.definitionLoads <- struct{}{}:
		defer func() { <-c.definitionLoads }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if definition := c.cachedDefinition(name, digest, nativeAgent); definition != nil {
		span.SetAttributes(attribute.Bool("toolregistry.catalog.definition_cache_hit", true))
		return definition, nil
	}
	definition, err := c.prepareDefinition(name, raw, nativeAgent)
	if err != nil {
		return nil, err
	}
	definition.digest = digest
	c.definitionsMu.Lock()
	c.definitions[name] = definition
	c.definitionsMu.Unlock()
	return definition, nil
}

// prepareDefinition receives exact saved JSON, checks its original member names
// and fingerprint, and returns an owned declaration with compiled schemas. Native
// admission and cold reads use these same rules before retaining the result.
func (c *toolsetCatalog) prepareDefinition(name, raw string, nativeAgent bool) (*catalogDefinition, error) {
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
	definition.nativeAgent = nativeAgent
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
