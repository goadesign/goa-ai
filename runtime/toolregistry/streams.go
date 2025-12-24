package toolregistry

import "fmt"

// ToolsetStreamID returns the deterministic Pulse stream identifier used for
// publishing tool call messages to providers for the given toolset registration ID.
func ToolsetStreamID(toolset string) string {
	return fmt.Sprintf("toolset:%s:requests", toolset)
}

// ResultStreamID returns the deterministic Pulse stream identifier used for
// publishing a single tool result message for the given tool use identifier.
func ResultStreamID(toolUseID string) string {
	return fmt.Sprintf("result:%s", toolUseID)
}


