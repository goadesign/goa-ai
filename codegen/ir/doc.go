// Package ir provides a stable, deterministic intermediate representation (IR)
// used as the sole input to goa-ai code generation templates.
//
// The IR is constructed from evaluated Goa roots and exists to decouple template
// rendering from Goa expression graphs, enabling cleaner generation logic and
// more predictable outputs.
package ir
