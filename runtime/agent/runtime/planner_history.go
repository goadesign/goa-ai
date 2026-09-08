package runtime

// Planner activities prepare conversation messages only when the planner asks
// for them. One activity shares one prepared value and failure; neither is
// reused by another activity or written into workflow history.

import (
	"context"
	"sync"

	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// plannerHistory retains the preparation failure separately so planner code
	// cannot ignore it and admit a decision based on invalid history.
	plannerHistory struct {
		prepare func() ([]*model.Message, error)
		mu      sync.Mutex
		err     error
	}
)

func (r *Runtime) newPlannerHistory(ctx context.Context, reg *AgentRegistration, messages []*model.Message, definitions []*model.ToolDefinition) *plannerHistory {
	history := &plannerHistory{}
	history.prepare = sync.OnceValues(func() ([]*model.Message, error) {
		prepared, err := r.applyHistoryPolicy(ctx, reg, messages, definitions)
		history.mu.Lock()
		history.err = err
		history.mu.Unlock()
		return prepared, err
	})
	return history
}

// preparationError reads the recorded result without causing unused history
// work. Planner calls must finish before the activity inspects this result.
func (h *plannerHistory) preparationError() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}
