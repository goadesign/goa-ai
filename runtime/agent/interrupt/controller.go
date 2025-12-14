// Package interrupt provides workflow signal handling for pausing and resuming
// agent runs. It exposes a Controller that workflows can use to react to
// external pause/resume requests via workflow engine signals.
package interrupt

import (
	"context"
	"errors"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

type (
	// PauseRequest carries metadata attached to a pause signal.
	PauseRequest struct {
		// RunID identifies the run to pause.
		RunID string
		// Reason describes why the run is being paused (for example, "user_requested").
		Reason string
		// RequestedBy identifies the logical actor requesting the pause (for example, a user
		// ID, service name, or "policy_engine").
		RequestedBy string
		// Labels carries optional key-value metadata associated with the pause request.
		Labels map[string]string
		// Metadata carries arbitrary structured data attached to the pause request.
		Metadata map[string]any
	}

	// ResumeRequest carries metadata attached to a resume signal.
	ResumeRequest struct {
		// RunID identifies the run to resume.
		RunID string
		// Notes carries optional human-readable context provided when resuming the run.
		Notes string
		// RequestedBy identifies the logical actor requesting the resume.
		RequestedBy string
		// Labels carries optional key-value metadata associated with the resume request.
		Labels map[string]string
		// Messages allows human or policy actors to inject new conversational messages
		// before the planner resumes execution.
		Messages []*model.Message
	}

	// ClarificationAnswer carries a typed answer for a paused clarification request.
	ClarificationAnswer struct {
		// RunID identifies the run associated with the clarification.
		RunID string
		// ID is the clarification await identifier.
		ID string
		// Answer is the free-form clarification text provided by the actor.
		Answer string
		// Labels carries optional metadata associated with the clarification answer.
		Labels map[string]string
	}

	// ConfirmationDecision carries a typed decision for a confirmation await.
	ConfirmationDecision struct {
		// RunID identifies the run associated with the confirmation.
		RunID string
		// ID is the confirmation await identifier.
		ID string
		// Approved is true when the operator approved the pending action.
		Approved bool
		// RequestedBy identifies the logical actor that provided the decision.
		RequestedBy string
		// Labels carries optional metadata associated with the decision.
		Labels map[string]string
		// Metadata carries arbitrary structured data for audit trails (for example,
		// ticket IDs or justification codes).
		Metadata map[string]any
	}

	// ToolResultsSet carries results for an external tools await request.
	ToolResultsSet struct {
		// RunID identifies the run associated with the external tool results.
		RunID string
		// ID is the await identifier corresponding to the original AwaitExternalTools event.
		ID string
		// Results contains the tool results provided by an external system.
		Results []*planner.ToolResult
		// RetryHints optionally provides hints associated with failures.
		RetryHints []*planner.RetryHint
	}

	// Controller drains runtime interrupt signals and exposes helpers the
	// workflow loop can call to react to pause/resume and await requests.
	Controller struct {
		pauseCh   engine.SignalChannel
		resumeCh  engine.SignalChannel
		clarifyCh engine.SignalChannel
		resultsCh engine.SignalChannel
		confirmCh engine.SignalChannel
	}
)

const (
	// SignalPause is the workflow signal name used to pause a run.
	SignalPause = "goaai.runtime.pause"
	// SignalResume is the workflow signal name used to resume a paused run.
	SignalResume = "goaai.runtime.resume"

	// SignalProvideClarification delivers a ClarificationAnswer to a waiting run.
	SignalProvideClarification = "goaai.runtime.provide.clarification"
	// SignalProvideToolResults delivers external tool results to a waiting run.
	SignalProvideToolResults = "goaai.runtime.provide.toolresults"
	// SignalProvideConfirmation delivers a ConfirmationDecision to a waiting run.
	SignalProvideConfirmation = "goaai.runtime.provide.confirmation"
)

// NewController builds a controller wired to the workflow context signals.
func NewController(wfCtx engine.WorkflowContext) *Controller {
	return &Controller{
		pauseCh:   wfCtx.SignalChannel(SignalPause),
		resumeCh:  wfCtx.SignalChannel(SignalResume),
		clarifyCh: wfCtx.SignalChannel(SignalProvideClarification),
		resultsCh: wfCtx.SignalChannel(SignalProvideToolResults),
		confirmCh: wfCtx.SignalChannel(SignalProvideConfirmation),
	}
}

// PollPause attempts to dequeue a pause request without blocking.
func (c *Controller) PollPause() (PauseRequest, bool) {
	if c == nil || c.pauseCh == nil {
		return PauseRequest{}, false
	}
	var req PauseRequest
	if !c.pauseCh.ReceiveAsync(&req) {
		return PauseRequest{}, false
	}
	return req, true
}

// WaitResume blocks until a resume request is delivered. Returns an error if the
// controller was not initialized with a resume signal channel.
func (c *Controller) WaitResume(ctx context.Context) (ResumeRequest, error) {
	if c == nil || c.resumeCh == nil {
		return ResumeRequest{}, errors.New("interrupt: resume channel unavailable")
	}
	var req ResumeRequest
	if err := c.resumeCh.Receive(ctx, &req); err != nil {
		return ResumeRequest{}, err
	}
	return req, nil
}

// WaitProvideClarification blocks until a clarification answer is delivered.
func (c *Controller) WaitProvideClarification(ctx context.Context) (ClarificationAnswer, error) {
	if c == nil || c.clarifyCh == nil {
		return ClarificationAnswer{}, errors.New("interrupt: clarification channel unavailable")
	}
	var ans ClarificationAnswer
	if err := c.clarifyCh.Receive(ctx, &ans); err != nil {
		return ClarificationAnswer{}, err
	}
	return ans, nil
}

// WaitProvideToolResults blocks until external tool results are delivered.
func (c *Controller) WaitProvideToolResults(ctx context.Context) (ToolResultsSet, error) {
	if c == nil || c.resultsCh == nil {
		return ToolResultsSet{}, errors.New("interrupt: results channel unavailable")
	}
	var rs ToolResultsSet
	if err := c.resultsCh.Receive(ctx, &rs); err != nil {
		return ToolResultsSet{}, err
	}
	return rs, nil
}

// WaitProvideConfirmation blocks until a confirmation decision is delivered.
func (c *Controller) WaitProvideConfirmation(ctx context.Context) (ConfirmationDecision, error) {
	if c == nil || c.confirmCh == nil {
		return ConfirmationDecision{}, errors.New("interrupt: confirmation channel unavailable")
	}
	var dec ConfirmationDecision
	if err := c.confirmCh.Receive(ctx, &dec); err != nil {
		return ConfirmationDecision{}, err
	}
	return dec, nil
}
