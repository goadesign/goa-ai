# Standalone JSON codecs

Generate strict JSON encoding and decoding for an existing named Goa type
without declaring a tool, completion, or service method for serialization.
The original service package continues to own the Go type. The codec plugin
adds typed functions in a separate generated package.

## Select a type

Import the plugin from the design package and mark the original declaration:

```go
package design

import (
	_ "goa.design/goa-ai/codegen/jsoncodec"
	. "goa.design/goa/v3/dsl"
)

var Settings = Type("Settings", func() {
	Meta("struct:pkg:path", "types")
	Meta("type:generate:force")
	Meta("goa-ai:json:codec")
	Field(1, "name", String, "The saved configuration name.", func() {
		MinLength(1)
	})
	Required("name")
})
```

The type must have an explicit generated package location and already belong
to Goa's generated type catalog. A service reference can make it reachable;
`type:generate:force` keeps an otherwise unused declaration in that catalog.
The codec marker takes no values and belongs only on an original named type
declaration. It is not a service, method, or field option.

Run the application's normal `goa gen` command. For the example above, generation
adds `gen/types/jsoncodec` with these functions:

```go
func EncodeSettings(value *gentypes.Settings) ([]byte, error)
func DecodeSettings(data []byte) (*gentypes.Settings, error)
```

Here `gentypes` refers to the application's existing `gen/types` package.
Parameters and results use the original type, including its package and Go
representation. Other root shapes use their corresponding Go references.
Private transport types and validation functions live beneath the codec
package's `internal` directory. Callers need only the two public functions.

Selection does not add codec APIs for unmarked parents or children, change the
service's types, or register a model-visible capability. Types needed inside a
selected value receive only the private support required by that codec.

## Encoding and decoding

`EncodeSettings` validates the original typed value before conversion can erase
invalid required nil fields. It checks text and cycles, applies Goa constraints,
converts to the JSON representation, and returns bytes only after validation
succeeds. Encoding leaves the caller's value unchanged.

`DecodeSettings` checks the original JSON before typed decoding can discard
unknown fields, replace malformed text, or overwrite duplicate object members.
It rejects duplicate decoded member names, undeclared object fields, invalid
UTF-8, unpaired Unicode surrogate escapes, multiple JSON documents, wrong JSON
types, and violations of the selected Goa contract. Field names are exact and
case-sensitive. Maps retain their declared dynamic string keys.

Required fields and length constraints remain distinct. A required collection
cannot be nil, but an empty collection is valid unless its Goa constraints
forbid it. Collection element nullability follows the declared element contract.
For a union, the discriminator selects the branch whose fields and constraints
are checked. Named types retain the constraints of their underlying definitions.

The supported values are closed generated Go representations: primitives,
objects, arrays, maps with string or named-string keys, unions, and their named
forms. Finite recursive values are supported; cyclic Go values are rejected
before recursive conversion. Generation rejects `Any`, custom Go representation
metadata, and non-string map keys instead of guessing their JSON behavior.

Failures return an error rather than repaired data. These codecs do not impose
an application-specific byte or work limit. The calling application owns its
request and storage budgets.

## Adoption

Adding or changing a marker requires regeneration and rebuilding the caller.
No runtime flag enables the codec. Existing tool and completion codecs keep
their own contracts; selecting a standalone value does not silently change
their acceptance rules.

The codec does not migrate previously stored application data. Before replacing
an existing serializer, verify that retained documents satisfy the declared Goa
contract and handle any required conversion in the application that owns them.
