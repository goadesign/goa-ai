// Package interrupt provides workflow signal handling for pausing and resuming
// agent runs. It exposes a Controller that workflows can use to react to
// external pause/resume requests via Temporal signals.
package interrupt

import (
	"context"
	"errors"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
)

const (
	// SignalPause is the workflow signal name used to pause a run.
	SignalPause = "goaai.runtime.pause"
	// SignalResume is the workflow signal name used to resume a paused run.
	SignalResume = "goaai.runtime.resume"
)

type (
	// PauseRequest carries metadata attached to a pause signal.
	PauseRequest struct {
		RunID       string
		Reason      string
		RequestedBy string
		Labels      map[string]string
		Metadata    map[string]any
	}

	// ResumeRequest carries metadata attached to a resume signal.
	ResumeRequest struct {
		RunID       string
		Notes       string
		RequestedBy string
		Labels      map[string]string
		// Messages allows human or policy actors to inject new conversational
		// messages before the planner resumes execution.
		Messages []planner.AgentMessage
	}

	// Controller drains runtime interrupt signals and exposes helpers the
	// workflow loop can call to react to pause/resume requests.
	Controller struct {
		pauseCh  engine.SignalChannel
		resumeCh engine.SignalChannel
	}
)

// NewController builds a controller wired to the workflow context signals.
func NewController(wfCtx engine.WorkflowContext) *Controller {
	return &Controller{
		pauseCh:  wfCtx.SignalChannel(SignalPause),
		resumeCh: wfCtx.SignalChannel(SignalResume),
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
