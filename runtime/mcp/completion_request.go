// Package mcp checks raw completion fields before typed decoding. JSON null
// must not become a valid zero string or an absent context inside the service.
// Ordinary field types and required fields remain owned by generated validators.
package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// validateCompletionRequest rejects null context and non-string prior values.
// The generated reference and argument types check their remaining fields.
func validateCompletionRequest(params map[string]json.RawMessage) *Error {
	raw, present := params["context"]
	if !present {
		return nil
	}
	var context map[string]json.RawMessage
	if json.Unmarshal(raw, &context) != nil || context == nil {
		return &Error{Code: JSONRPCInvalidParams, Message: "completion context must be an object"}
	}
	raw, present = context["arguments"]
	if !present {
		return nil
	}
	return validateStringArguments(raw, "completion context")
}

// validateStringArguments accepts a raw object of strings, preserving empty
// strings while rejecting JSON null before Go decoding can erase its presence.
func validateStringArguments(raw json.RawMessage, kind string) *Error {
	var arguments map[string]json.RawMessage
	if json.Unmarshal(raw, &arguments) != nil || arguments == nil {
		return &Error{Code: JSONRPCInvalidParams, Message: fmt.Sprintf("%s arguments must be an object of strings", kind)}
	}
	for name, value := range arguments {
		var text string
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &text) != nil {
			return &Error{Code: JSONRPCInvalidParams, Message: fmt.Sprintf("%s argument %q must be a string", kind, name)}
		}
	}
	return nil
}
