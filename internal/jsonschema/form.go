// Package jsonschema owns the MCP form schema subset used during generation and when
// receiving external forms. Unsupported fields fail instead of losing rules.
package jsonschema

import (
	"fmt"
	"sync"

	schema "github.com/santhosh-tekuri/jsonschema/v6"
)

var compileFormSchema = sync.OnceValues(func() (*schema.Schema, error) {
	return Compile([]byte(formSchemaContract))
})

// ValidateForm checks both MCP's restricted form shape and the schema itself.
// Generators and runtime consumers receive the same error for unsupported rules.
func ValidateForm(raw []byte) error {
	contract, err := compileFormSchema()
	if err != nil {
		return err
	}
	if err := Validate(contract, raw); err != nil {
		return fmt.Errorf("unsupported form schema: %w", err)
	}
	_, err = Compile(raw)
	return err
}

const formSchemaContract = `{
 "$schema":"https://json-schema.org/draft/2020-12/schema",
 "type":"object","required":["type","properties"],"additionalProperties":false,
 "properties":{
  "$schema":{"type":"string"},"type":{"const":"object"},
  "properties":{"type":"object","additionalProperties":{"$ref":"#/$defs/field"}},
  "required":{"type":"array","items":{"type":"string"},"uniqueItems":true}
 },
 "$defs":{
  "choice":{"type":"object","required":["const","title"],"additionalProperties":false,"properties":{"const":{"type":"string"},"title":{"type":"string"}}},
  "choices":{"type":"array","minItems":1,"items":{"$ref":"#/$defs/choice"}},
  "field":{"anyOf":[
   {"type":"object","required":["type"],"additionalProperties":false,"properties":{
    "type":{"const":"string"},"title":{"type":"string"},"description":{"type":"string"},"default":{"type":"string"},
    "minLength":{"type":"integer","minimum":0},"maxLength":{"type":"integer","minimum":0},
    "format":{"enum":["email","uri","date","date-time"]},
    "enum":{"type":"array","minItems":1,"items":{"type":"string"}},"oneOf":{"$ref":"#/$defs/choices"}
   }},
   {"type":"object","required":["type"],"additionalProperties":false,"properties":{
    "type":{"enum":["number","integer"]},"title":{"type":"string"},"description":{"type":"string"},"default":{"type":"number"},"minimum":{"type":"number"},"maximum":{"type":"number"}
   }},
   {"type":"object","required":["type"],"additionalProperties":false,"properties":{
    "type":{"const":"boolean"},"title":{"type":"string"},"description":{"type":"string"},"default":{"type":"boolean"}
   }},
   {"type":"object","required":["type","items"],"additionalProperties":false,"properties":{
    "type":{"const":"array"},"title":{"type":"string"},"description":{"type":"string"},"default":{"type":"array","items":{"type":"string"}},
    "minItems":{"type":"integer","minimum":0},"maxItems":{"type":"integer","minimum":0},
    "items":{"anyOf":[
     {"type":"object","required":["type","enum"],"additionalProperties":false,"properties":{"type":{"const":"string"},"enum":{"type":"array","minItems":1,"items":{"type":"string"}}}},
     {"type":"object","required":["anyOf"],"additionalProperties":false,"properties":{"anyOf":{"$ref":"#/$defs/choices"}}}
    ]}
   }}
  ]}
 }
}`
