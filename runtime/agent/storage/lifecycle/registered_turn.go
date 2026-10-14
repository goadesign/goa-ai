// Package lifecycle gives registered workflow builders and storage checks the same turn identity.
// A new Agent continuation starts a new turn; service tools have no model turn.
package lifecycle

import (
	"errors"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
)

// RegisteredRunTurnID derives the turn used by a registered execution from its
// executor and immediate predecessor. Initial executions and service tools have
// no turn; an Agent continuation uses its new execution ID.
func RegisteredRunTurnID(subject genrunidentity.RunSubject, runID, predecessorRunID string) (string, error) {
	if _, err := genrunidentity.EncodeRunSubject(&subject); err != nil {
		return "", err
	}
	if runID == "" {
		return "", errors.New("registered run ID is required")
	}
	if predecessorRunID == runID {
		return "", errors.New("registered predecessor must differ from run ID")
	}
	if _, agent := subject.Executor.AsAgent(); agent && predecessorRunID != "" {
		return runID, nil
	}
	return "", nil
}
