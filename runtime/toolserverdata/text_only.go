// Package toolserverdata validates output before a tool result is published.
package toolserverdata

import (
	"fmt"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

// ValidateTextOnly rejects UI output while preserving internal computation and
// evidence records. It validates the common envelope, including unknown audiences.
func ValidateTextOnly(data rawjson.Message) error {
	items, err := decodeEnvelope(data)
	if err != nil {
		return err
	}
	for _, item := range items {
		switch item.Audience {
		case "internal", "evidence":
		case "timeline":
			return fmt.Errorf("UI output %q is forbidden", item.Kind)
		default:
			return fmt.Errorf("unknown server data audience %q", item.Audience)
		}
	}
	return nil
}
