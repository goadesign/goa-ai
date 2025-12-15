package runtime

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// generateRunID returns a globally unique run identifier suitable for use as a
// workflow engine execution ID (for example a Temporal WorkflowID).
//
// The generated identifier is prefixed with a normalized agent ID to improve
// observability in logs, metrics, and tracing without sacrificing uniqueness.
func generateRunID(agentID string) string {
	prefix := strings.ReplaceAll(agentID, ".", "-")
	return fmt.Sprintf("%s-%s", prefix, uuid.NewString())
}
