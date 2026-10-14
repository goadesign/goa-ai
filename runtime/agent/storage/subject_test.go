// These tests exercise the shared store entry points with both generated run
// identities and reject invalid subjects before any store owns their records.
package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/runlog"
)

func TestStorageSubjectValidation(t *testing.T) {
	for _, test := range []struct {
		name    string
		subject genrunidentity.RunSubject
		valid   bool
	}{
		{name: "Agent", subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}, valid: true},
		{name: "service", subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "catalog.lookup"})}, valid: true},
		{name: "unset"},
		{name: "empty Agent", subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("")}},
		{name: "missing service", subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(nil)}},
		{name: "missing registry", subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Tool: "catalog.lookup"})}},
		{name: "unqualified tool", subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "lookup"})}},
	} {
		t.Run(test.name, func(t *testing.T) {
			errors := []error{
				ValidatePreparationOperation(PreparationOperation{Subject: test.subject, RunID: "run", CommandID: "command"}),
				ValidateSeedDeclaration(SeedDeclaration{Origin: genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: "run"})}, Subject: test.subject, RunID: "run", CommandID: "command", AttemptID: "attempt", Kind: SeedLiteral}),
				ValidateRunRecord(&runlog.Event{Subject: test.subject, RunID: "run", EventKey: "event", Type: "note", Payload: []byte(`{}`), Timestamp: time.Now().UTC().Truncate(time.Millisecond)}),
			}
			for _, err := range errors {
				if test.valid {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			}
		})
	}
}

func TestServiceSubjectCannotReferenceModelHistory(t *testing.T) {
	for _, kind := range []SeedKind{SeedNextTurn, SeedContinuation} {
		declaration := SeedDeclaration{
			Origin:  genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: "next"})},
			Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "catalog.lookup"})},
			RunID:   "next", SessionID: "session", CommandID: "command", AttemptID: "attempt", Kind: kind, SourceRunID: "source",
		}
		if kind == SeedContinuation {
			declaration.SourceEndID = "1"
		}
		require.ErrorContains(t, ValidateSeedDeclaration(declaration), "referenced model history requires an Agent subject")
	}
}
