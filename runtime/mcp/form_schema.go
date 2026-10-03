// Package mcp describes the protocol's form schema subset. The same JSON Schema
// compiler validates these incoming contracts and their accepted host answers.
package mcp

import (
	"sync"

	schema "github.com/santhosh-tekuri/jsonschema/v6"

	"goa.design/goa-ai/internal/jsonschema"
)

var compileFormSchema = sync.OnceValues(func() (*schema.Schema, error) {
	compiled, err := jsonschema.Compile([]byte(formSchemaContract))
	return compiled, err
})

// Accepted form values remain flat even when the requested schema permits extra keys.
var compileFormContent = sync.OnceValues(func() (*schema.Schema, error) {
	return jsonschema.Compile([]byte(`{"type":"object","additionalProperties":{"anyOf":[{"type":"string"},{"type":"number"},{"type":"boolean"},{"type":"array","items":{"type":"string"}}]}}`))
})

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
