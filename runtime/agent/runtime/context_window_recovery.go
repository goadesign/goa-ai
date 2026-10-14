// Package runtime uses the existing history policy after a context-limit stop before the workflow
// invokes the planner again. It retains original evidence and refuses another
// inference when summarization cannot shorten the destination request.
package runtime

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"goa.design/goa-ai/runtime/agent/internal/outputcontract"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

type requiredHistoryCompactionKey struct{}

const contextWindowStopReason = "model_context_window_exceeded"

// requireHistoryCompaction marks only this recovery request for the existing
// policy and verifies that it replaced older evidence with a summary.
func requireHistoryCompaction(policy HistoryPolicy) HistoryPolicy {
	return func(ctx context.Context, request *model.Request, counter model.TokenCounter, prior *HistorySummary) (HistoryResult, error) {
		ctx = context.WithValue(ctx, requiredHistoryCompactionKey{}, true)
		result, err := policy(ctx, request, counter, prior)
		if err != nil {
			return HistoryResult{}, err
		}
		if result.Summary == nil || (prior != nil && result.Summary.ReplacedMessages <= prior.ReplacedMessages) {
			return HistoryResult{}, errors.New("context-window recovery requires a summary replacing additional older messages")
		}
		previous := *request
		if prior != nil {
			previous.Messages, err = historySummaryMessages(request.Messages, prior)
			if err != nil {
				return HistoryResult{}, err
			}
		}
		if err := verifyHistoryCompaction(ctx, counter, &previous, result.Messages); err != nil {
			return HistoryResult{}, err
		}
		return result, nil
	}
}

// verifyHistoryCompaction counts the shortened destination request and records
// progress. Estimates remain estimates; a second context stop still consumes
// the workflow's existing recovery budget rather than implying a guaranteed fit.
func verifyHistoryCompaction(ctx context.Context, counter model.TokenCounter, before *model.Request, messages []*model.Message) error {
	previous, err := counter.CountTokens(ctx, before)
	if err != nil {
		return err
	}
	after := *before
	after.Messages = messages
	current, err := counter.CountTokens(ctx, &after)
	if err != nil {
		return err
	}
	if current.InputTokens >= previous.InputTokens {
		return errors.New("context-window recovery summary did not reduce the destination input token count")
	}
	trace.SpanFromContext(ctx).AddEvent("history.context_window_recovery", trace.WithAttributes(
		attribute.Int("history.input_tokens.before", previous.InputTokens),
		attribute.Int("history.input_tokens.after", current.InputTokens),
		attribute.Bool("history.token_count.exact", current.Exact),
	))
	return nil
}

// contextWindowFailure checks the designated completed invocation before its
// result reaches planner code. Its response and usage stay in the journal;
// only the workflow decides whether another request may run.
func (j *modelInvocationJournal) contextWindowFailure() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	candidate := j.invocations[j.designated]
	if candidate == nil || candidate.response == nil || candidate.response.StopReason != contextWindowStopReason {
		return nil
	}
	return j.rejectContextWindowResponse(j.designated)
}

// rejectContextWindowResponse selects the exact completed response that needs
// compaction, preserving its evidence and error for the workflow. The caller
// holds the journal lock.
func (j *modelInvocationJournal) rejectContextWindowResponse(id modelInvocationID) error {
	candidate := j.invocations[id]
	err := outputcontract.NewWithOrigin(errors.New("selected model response reached its context window limit"), planner.OutputContractOriginModel)
	candidate.rejectedOutputErr = err
	evidence := candidate.responseEvidence
	candidate.rejectedResponseEvidence = &evidence
	j.selected = id
	j.recovery = id
	j.outputErr = err
	return err
}
