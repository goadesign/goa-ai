package session

// This file checks the immutable run-start values accepted by every durable
// Store implementation.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
)

func TestValidateRunStartRequiresMillisecondPrecision(t *testing.T) {
	t.Parallel()

	start := RunStart{
		Subject:   genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("service.agent"))},
		RunID:     "run-1",
		SessionID: "session-1",
		StartedAt: time.Date(2026, time.August, 29, 12, 0, 0, 1, time.UTC),
	}

	require.EqualError(t, ValidateRunStart(start, false), "started_at must use millisecond precision")
	start.StartedAt = start.StartedAt.Truncate(time.Millisecond)
	require.NoError(t, ValidateRunStart(start, false))

	start.PredecessorRunID = start.RunID
	require.EqualError(t, ValidateRunStart(start, false), "predecessor run id must differ from run id")
}

func TestRunStartSubjectValidation(t *testing.T) {
	agentSubject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("agent"))}
	serviceSubject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "catalog.lookup"})}
	for _, subject := range []genrunidentity.RunSubject{agentSubject, serviceSubject} {
		start := RunStart{Subject: subject, RunID: "run", SessionID: "session", StartedAt: time.Now().UTC().Truncate(time.Millisecond)}
		require.NoError(t, ValidateRunStart(start, false))
		start.ParentRunID = "parent"
		if subject.Executor.Kind() == genrunidentity.RunExecutorKindAgent {
			require.NoError(t, ValidateRunStart(start, true))
		} else {
			require.ErrorIs(t, ValidateRunStart(start, true), ErrChildAgentIDRequired)
		}
	}
	for _, subject := range []genrunidentity.RunSubject{
		{},
		{Executor: genrunidentity.NewRunExecutorAgent("")},
		{Executor: genrunidentity.NewRunExecutorServiceTool(nil)},
		{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Tool: "catalog.lookup"})},
		{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "lookup"})},
	} {
		require.Error(t, ValidateRunStart(RunStart{Subject: subject, RunID: "run", SessionID: "session", StartedAt: time.Now().UTC().Truncate(time.Millisecond)}, false))
	}
}
