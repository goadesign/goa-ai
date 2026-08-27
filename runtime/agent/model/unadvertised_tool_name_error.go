// This file defines the error marker used when a provider returns a valid tool
// name that was not advertised in the current request. Provider adapters add
// the marker at their exact request-local lookup, and callers can retain the
// rejected name without parsing provider-specific error text.

package model

import (
	"errors"
	"fmt"
)

type unadvertisedToolNameError struct {
	name string
}

// NewUnadvertisedToolNameError marks a provider-returned tool name that was
// absent from the exact tool catalog sent with the request. name must be the
// untouched name returned by the provider, even when the adapter normalizes a
// separate copy for lookup.
func NewUnadvertisedToolNameError(name string) error {
	if name == "" {
		panic("model: unadvertised tool name is required")
	}
	return &unadvertisedToolNameError{name: name}
}

// UnadvertisedToolName returns the exact rejected tool name carried by err. It
// follows wrapped errors, including an OutputValidationError reconstructed by
// a trusted transport.
func UnadvertisedToolName(err error) (string, bool) {
	var marker *unadvertisedToolNameError
	if !errors.As(err, &marker) {
		return "", false
	}
	return marker.name, true
}

// Error identifies the rejected name for diagnostics. Callers use
// UnadvertisedToolName instead of parsing this text.
func (e *unadvertisedToolNameError) Error() string {
	return fmt.Sprintf("tool name %q was not advertised", e.name)
}
