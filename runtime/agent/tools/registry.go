// Package tools defines the execution data retained for a tool selected from a
// registry. Model requests never contain this record; the runtime attaches it
// after validating the selection against the current planning catalog.
package tools

import (
	"slices"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	// RegistryBinding retains the registration and definitions selected for one
	// call. It travels with that call through execution, confirmation, and replay.
	// A new planning activity resolves its own catalog independently.
	RegistryBinding struct {
		// Registry is the registry source named by the consuming agent's design.
		Registry string `json:"registry"`
		// Resolution is a generated registry ResolveToolsetResponse encoded as
		// protobuf JSON, so saved data uses the generated transport validator.
		// It contains only the selected tool and any declared pagination partner,
		// with the registration token returned by the original catalog read.
		Resolution rawjson.Message `json:"resolution"`
	}
)

// Clone gives a new owner independent saved definition bytes. A nil binding
// identifies a static tool and remains nil.
func (b *RegistryBinding) Clone() *RegistryBinding {
	if b == nil {
		return nil
	}
	return &RegistryBinding{Registry: b.Registry, Resolution: slices.Clone(b.Resolution)}
}
