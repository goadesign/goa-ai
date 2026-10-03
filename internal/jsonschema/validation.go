// Package jsonschema compiles one in-memory JSON Schema and validates exact JSON
// numbers. Local references may recurse; external files and network references
// are rejected before any value reaches a tool executor or host interaction.
package jsonschema

import (
	"bytes"
	"fmt"

	schema "github.com/santhosh-tekuri/jsonschema/v6"
)

type (
	// rawLoader supplies only the document owned by this compilation.
	rawLoader struct{ document []byte }
)

const resource = "schema://goa-ai/contract.json"

// Compile retains a private validator for the complete 2020-12 document. A
// declared older dialect is honored by the compiler; it never retrieves $refs.
func Compile(document []byte) (*schema.Schema, error) {
	compiler := schema.NewCompiler()
	compiler.DefaultDraft(schema.Draft2020)
	compiler.UseLoader(rawLoader{document: document})
	compiled, err := compiler.Compile(resource)
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

func (l rawLoader) Load(url string) (any, error) {
	if url != resource {
		return nil, fmt.Errorf("external schema reference %q is not allowed", url)
	}
	return schema.UnmarshalJSON(bytes.NewReader(l.document))
}
