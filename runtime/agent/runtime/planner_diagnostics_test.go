package runtime

// Planner diagnostics must describe rejection without changing execution or
// correction. These tests exercise real activity entry points and the workflow
// codec, including old records whose missing text cannot be reconstructed.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/errorevidence"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
)

func TestPlannerActivitiesOfferOriginalRejectionToTracer(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume=%t", resume), func(t *testing.T) {
			original := planner.NewOutputContractError(fmt.Errorf("validate result: %w", errors.New(strings.Repeat("field[7] is invalid\n", 400))))
			pl := &stubPlanner{
				start:  func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) { return nil, original },
				resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) { return nil, original },
			}
			rt := newTestRuntimeWithPlanner("service.agent", pl)
			tracer := &recordingTelemetryTracer{}
			rt.tracer = tracer
			input := &PlanActivityInput{AgentID: "service.agent", RunID: "run", RunContext: run.Context{RunID: "run", TurnID: "turn"}}
			var output *PlanActivityOutput
			var err error
			if resume {
				output, err = rt.PlanResumeActivity(t.Context(), seedTestPlanInput(t, rt, *(input), nil))
			} else {
				output, err = rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, *(input), nil))
			}
			require.NoError(t, err)
			require.NotNil(t, output.OutputContractFailure)
			assert.Equal(t, original.Unwrap().Error(), output.OutputContractFailure.Reason)
			activityName := "planner.plan_start"
			if resume {
				activityName = "planner.plan_resume"
			}
			span := rejectedPlannerActivitySpan(t, tracer, activityName)
			assert.Same(t, original, span.errs[0])
		})
	}
}

func TestPlannerFullReasonRespectsCompleteResultBudget(t *testing.T) {
	for _, size := range []int{64 * 1024, engine.MaxPayloadBytes + 1} {
		original := planner.NewOutputContractError(errors.New(strings.Repeat("x", size)))
		rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
			return nil, original
		}})
		tracer := &recordingTelemetryTracer{}
		rt.tracer = tracer
		out, err := rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, PlanActivityInput{AgentID: "service.agent", RunID: "run", RunContext: run.Context{RunID: "run", TurnID: "turn"}}, nil))
		require.NoError(t, err)
		require.NotNil(t, out.OutputContractFailure)
		assert.Empty(t, out.OutputContractFailure.ReasonOmitted)
		if size < engine.MaxPayloadBytes {
			assert.Equal(t, original.Unwrap().Error(), out.OutputContractFailure.Reason)
		} else {
			assert.Contains(t, out.OutputContractFailure.Reason, "planner activity output rejected before Temporal encoding")
			assert.NotEqual(t, original.Unwrap().Error(), out.OutputContractFailure.Reason)
		}
		_, err = workflowcodec.NewDataConverter().ToPayload(out)
		require.NoError(t, err)
		span := rejectedPlannerActivitySpan(t, tracer, "planner.plan_start")
		assert.Same(t, original, span.errs[0])
	}
}

func TestPlannerSpanIncludesRuntimeAcceptance(t *testing.T) {
	rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
		return &planner.PlanResult{}, nil
	}})
	tracer := &recordingTelemetryTracer{}
	rt.tracer = tracer
	output, err := rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, PlanActivityInput{AgentID: "service.agent", RunID: "run", RunContext: run.Context{RunID: "run", TurnID: "turn"}}, nil))
	require.NoError(t, err)
	require.NotNil(t, output.OutputContractFailure)
	rejectedPlannerActivitySpan(t, tracer, "planner.plan_start")
	assert.NotEmpty(t, output.OutputContractFailure.Reason)
}

func TestPlannerReasonWorkflowCodecPreservesLegacyAndCurrent(t *testing.T) {
	codec := workflowcodec.NewDataConverter()
	for _, reason := range []string{"", "validate result: field[7] is invalid", strings.Repeat("é", errorevidence.MaxMessageBytes/2), strings.Repeat("x", errorevidence.MaxMessageBytes+1)} {
		for _, current := range []bool{false, true} {
			failure := terminalPlannerOutputContractFailure(errors.New(reason))
			if !current {
				failure.ReasonVersion, failure.Reason, failure.ReasonOmitted = "", "", ""
			}
			output := &api.PlanActivityOutput{OutputContractFailure: failure}
			payload, err := codec.ToPayload(output)
			require.NoError(t, err)
			var decoded api.PlanActivityOutput
			require.NoError(t, codec.FromPayload(payload, &decoded))
			require.NoError(t, validateOutputContractFailure(decoded.OutputContractFailure))
			reencoded, err := codec.ToPayload(&decoded)
			require.NoError(t, err)
			assert.Equal(t, payload.Data, reencoded.Data)
			reconstructed := boundedOutputContractError(decoded.OutputContractFailure)
			var classified *planner.OutputContractError
			require.ErrorAs(t, reconstructed, &classified)
			runFailure := hooks.RunFailureFromError(reconstructed)
			assert.Equal(t, hooks.PublicErrorOutputContract, runFailure.Message)
			assert.Equal(t, hooks.ErrorKindPlannerOutput, runFailure.Kind)
			assert.False(t, runFailure.Retryable)
			switch {
			case current && failure.ReasonOmitted == "":
				assert.Equal(t, reason, classified.Unwrap().Error())
				assert.Contains(t, runFailure.DebugMessage, reason)
			case current:
				assert.Contains(t, classified.Unwrap().Error(), "omitted")
			default:
				assert.NotContains(t, string(payload.Data), "ReasonVersion")
				assert.Contains(t, errors.Unwrap(reconstructed).Error(), "reason_sha256=")
				assert.Equal(t, "completed output does not meet its contract", runFailure.DebugMessage)
			}
		}
	}
}

func TestInvalidUTF8PlannerDiagnosticOmission(t *testing.T) {
	failure := terminalPlannerOutputContractFailure(errors.New(string([]byte{0xff})))
	codec := workflowcodec.NewDataConverter()
	payload, err := codec.ToPayload(&api.PlanActivityOutput{OutputContractFailure: failure})
	require.NoError(t, err)
	assert.NotContains(t, string(payload.Data), "�")
	var restored api.PlanActivityOutput
	require.NoError(t, codec.FromPayload(payload, &restored))
	assert.Equal(t, failure, restored.OutputContractFailure)
	assert.Equal(t, "invalid_utf8", failure.ReasonOmitted)
	assert.Empty(t, failure.Reason)
	require.NoError(t, validateOutputContractFailure(failure))
	runFailure := hooks.RunFailureFromError(boundedOutputContractError(failure))
	assert.Equal(t, hooks.ErrorKindPlannerOutput, runFailure.Kind)
	assert.False(t, runFailure.Retryable)
	assert.Contains(t, runFailure.DebugMessage, "invalid_utf8")
	assert.Contains(t, runFailure.DebugMessage, failure.ReasonSHA256)
}

// rejectedPlannerActivitySpan finds the single span for the whole activity and
// checks that preparation finished successfully before the planner result was
// rejected. It returns the activity span so callers can check the original error.
func rejectedPlannerActivitySpan(t *testing.T, tracer *recordingTelemetryTracer, activityName string) *recordingTelemetrySpan {
	t.Helper()
	preparationCounts := map[string]int{
		"planner.resolve_activity_input": 0,
		"planner.prepare_continuations":  0,
		"planner.prepare_invocation":     0,
	}
	if activityName == "planner.plan_resume" {
		preparationCounts["planner.load_tool_outputs"] = 0
	}
	var activities []*recordingTelemetrySpan
	for _, span := range tracer.spans {
		if span.name == activityName {
			activities = append(activities, span)
			continue
		}
		require.Contains(t, preparationCounts, span.name)
		preparationCounts[span.name]++
		assert.True(t, span.ended, span.name)
		assert.Equal(t, 1, span.endCount, span.name)
		assert.Equal(t, codes.Ok, span.statusCode, span.name)
		assert.Empty(t, span.errs, span.name)
	}
	for name, count := range preparationCounts {
		assert.Equal(t, 1, count, name)
	}
	require.Len(t, activities, 1)
	activity := activities[0]
	require.Len(t, activity.errs, 1)
	assert.Equal(t, codes.Error, activity.statusCode)
	assert.True(t, activity.ended)
	assert.Equal(t, 1, activity.endCount)
	return activity
}
