// Package jsonschema compiles one in-memory JSON Schema and validates exact JSON
// numbers. Local references may recurse; external files and network references
// are rejected before any value reaches a tool executor or host interaction.
package jsonschema

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/url"

	schema "github.com/santhosh-tekuri/jsonschema/v6"
)

type (
	// rawLoader rejects references outside the registered document.
	rawLoader struct{}
)

const resource = "schema://goa-ai/contract.json"

// Compile retains a private validator for the complete 2020-12 document. A
// declared older dialect is honored by the compiler; it never retrieves $refs.
func Compile(document []byte) (*schema.Schema, error) {
	value, err := schema.UnmarshalJSON(bytes.NewReader(document))
	if err != nil {
		return nil, fmt.Errorf("compile JSON Schema: %w", err)
	}
	location := resource
	if object, ok := value.(map[string]any); ok {
		// An absolute 2020-12 identifier supplies its own reference base.
		// Use a private content-derived retrieval address so a fixed address
		// cannot hide an embedded resource or select a built-in meta-schema.
		switch object["$schema"] {
		case nil, "https://json-schema.org/draft/2020-12/schema", "https://json-schema.org/draft/2020-12/schema#",
			"http://json-schema.org/draft/2020-12/schema", "http://json-schema.org/draft/2020-12/schema#":
			if id, ok := object["$id"].(string); ok {
				if parsed, err := url.Parse(id); err == nil && parsed.IsAbs() {
					location = fmt.Sprintf("schema://goa-ai/compiled/%x", sha256.Sum256(document))
				}
			}
		}
	}
	compiler := schema.NewCompiler()
	compiler.DefaultDraft(schema.Draft2020)
	compiler.UseLoader(rawLoader{})
	if err := compiler.AddResource(location, value); err != nil {
		return nil, fmt.Errorf("compile JSON Schema: %w", err)
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		return nil, fmt.Errorf("compile JSON Schema: %w", err)
	}
	return compiled, nil
}

// Validate decodes a single exact JSON value and checks the retained contract.
func Validate(compiled *schema.Schema, data []byte) error {
	value, err := schema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decode JSON value: %w", err)
	}
	if err := compiled.Validate(value); err != nil {
		return fmt.Errorf("validate JSON Schema: %w", err)
	}
	return nil
}

func (rawLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema reference %q is not allowed", url)
}
