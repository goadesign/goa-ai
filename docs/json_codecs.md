# Standalone JSON codecs

Generate strict JSON encoding and decoding for an existing named Goa type
without declaring a tool, completion, or service method for serialization.
With the Goa-AI DSL imported, normal `goa gen` adds typed functions beside each
supported original type. Goa continues to own type discovery, package placement,
and the type's Go representation.

## Generate codecs

Designs import only DSL packages. Types already used by service methods or
selected through tool and completion contracts need no codec metadata. For an
otherwise-unused type, use Goa's existing `type:generate:force`:

```go
package design

import (
	_ "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

var Settings = Type("Settings", func() {
	Meta("struct:pkg:path", "types")
	Meta("type:generate:force", "catalog")
	Field(1, "name", String, "The saved configuration name.", func() {
		MinLength(1)
	})
	Required("name")
})

var _ = Service("catalog", func() {})
```

The package location in this example is optional. An unlocated type remains in
the package Goa chooses for its service. Force metadata preserves its normal
service-name restrictions and requires an existing service emission context;
the codec does not create a synthetic service or make unused types reachable.

Run the application's normal `goa gen` command. For the example above, generation
adds these functions to `gen/types`:

```go
func EncodeSettings(value *Settings) ([]byte, error)
func DecodeSettings(data []byte) (*Settings, error)
```

Call them through the same package as the type, for example
`types.EncodeSettings(value)`. Parameters and results use the original Go
representation. Other root shapes use their corresponding Go references.
Function names use Goa's normal collision resolution. JSON helper types and
functions remain private within the original package.

Each supported original declaration receives its own pair, including reachable
nested types. A declaration shared by several services gets one pair in its
owning package; an original generated into separate service packages gets a
pair in each. Shared nested JSON helper declarations are reused within each
package. No codec subdirectory is reserved.

## Encoding and decoding

`EncodeSettings` validates the original typed value before conversion can erase
invalid required nil fields. It checks text and cycles, applies Goa constraints,
converts to the JSON representation, and returns bytes only after validation
succeeds. Encoding leaves the caller's value unchanged.

`DecodeSettings` checks the original JSON before typed decoding can discard
unknown fields, replace malformed text, or overwrite duplicate object members.
It rejects duplicate decoded member names, undeclared object fields, invalid
UTF-8, unpaired Unicode surrogate escapes, multiple JSON documents, wrong JSON
types, and violations of the original Goa contract. Field names are exact and
case-sensitive. Maps retain their declared dynamic string keys.

Required fields and length constraints remain distinct. A required collection
cannot be nil, but an empty collection is valid unless its Goa constraints
forbid it. Collection element nullability follows the declared element contract.
For a union, the discriminator selects the branch whose fields and constraints
are checked. Named types retain the constraints of their underlying definitions.

The supported values are closed generated Go representations: primitives,
objects, arrays, maps with string or named-string keys, unions, and their named
forms. Finite recursive values are supported; cyclic Go values are rejected
before recursive conversion.

Generation skips codec functions for a type if any reachable field or branch,
including an optional field, uses `Any`, custom Go representation metadata, or
non-string map keys. This skips the complete codec; it never drops fields from
an encoded value. Original type generation remains valid. Supported siblings
and nested types can still receive their own codecs. Runtime-owned builtin
forms such as Goa's service error do not receive original-type codecs.

Failures return an error rather than repaired data. These codecs do not impose
an application-specific byte or work limit. The calling application owns its
request and storage budgets.

## Adoption

Regenerate and rebuild to use these functions. Existing tool and completion
codecs keep their own contracts, including fields hidden from model inputs and
completion-owned result types. Complete-original codecs do not replace those
different representations.

The generated functions do not add JSON methods to original types or change
existing serialization automatically. Applications adopt them by calling the
typed encode and decode functions.

The codec does not migrate previously stored application data. Before replacing
an existing serializer, verify that retained documents satisfy the declared Goa
contract and handle any required conversion in the application that owns them.
