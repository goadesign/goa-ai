// Package toolregistry checks terminal provider output before it enters a result stream.
package toolregistry

import "fmt"

// ValidateTextOnlyResult rejects UI server data from a completed text-only call.
// Internal computation and evidence remain available to the accepting runtime.
func ValidateTextOnlyResult(result ToolResultMessage) error {
	if result.PendingExecution != nil {
		_, _, waiting := result.PendingExecution.AsTaskWait()
		if !waiting {
			return fmt.Errorf("text-only result cannot request host input")
		}
	}
	for _, item := range result.ServerData {
		if item == nil {
			return fmt.Errorf("text-only result has nil server data")
		}
		if item.Audience != "internal" && item.Audience != "evidence" {
			return fmt.Errorf("text-only result has unsupported server data %q for audience %q", item.Kind, item.Audience)
		}
	}
	return nil
}
