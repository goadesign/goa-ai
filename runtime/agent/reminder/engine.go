package reminder

import (
	"sort"
	"sync"
)

// Engine manages run-scoped reminders. It tracks per-run reminder state and
// enforces simple lifetime policies (per-run caps and turn-based rate
// limiting). Engines are safe for concurrent use.
//
// The Engine does not itself perform prompt injection; callers obtain the
// per-turn snapshot via Snapshot and pass reminders to planners or prompt
// builders as needed.
type Engine struct {
	mu   sync.RWMutex
	runs map[string]*runState
}

type runState struct {
	reminders map[string]*reminderState
	turnSeq   int
}

type reminderState struct {
	reminder Reminder
	emitted  int
	lastTurn int
}

// NewEngine constructs an Engine. Callers add reminders directly via
// AddReminder and obtain snapshots via Snapshot.
func NewEngine() *Engine {
	return &Engine{
		runs: make(map[string]*runState),
	}
}

// AddReminder registers or updates a reminder for the given run. When a
// reminder with the same ID already exists, its configuration is replaced
// while preserving emission counters so rate limiting continues to apply.
//
// Empty run IDs or reminder IDs are ignored.
func (e *Engine) AddReminder(runID string, r Reminder) {
	if runID == "" || r.ID == "" || r.Text == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	rs := e.ensureRunLocked(runID)
	if st, ok := rs.reminders[r.ID]; ok {
		st.reminder = r
		return
	}
	rs.reminders[r.ID] = &reminderState{
		reminder: r,
	}
}

// RemoveReminder removes a reminder with the given ID from a run. It is a
// no-op when the run or reminder is unknown.
func (e *Engine) RemoveReminder(runID, id string) {
	if runID == "" || id == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	rs, ok := e.runs[runID]
	if !ok || rs == nil {
		return
	}
	delete(rs.reminders, id)
}

// Snapshot returns the reminders that should be emitted for the next planner
// turn of the given run. It enforces per-run caps and turn-based rate limits,
// updates internal counters, and returns reminders ordered by priority tier
// (safety first) and ID for stability.
//
// When the run is unknown or has no active reminders, Snapshot returns nil.
func (e *Engine) Snapshot(runID string) []Reminder {
	if runID == "" {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	rs, ok := e.runs[runID]
	if !ok || rs == nil || len(rs.reminders) == 0 {
		return nil
	}
	rs.turnSeq++
	turn := rs.turnSeq
	out := make([]Reminder, 0, len(rs.reminders))
	for _, st := range rs.reminders {
		if !shouldEmit(st, turn) {
			continue
		}
		st.emitted++
		st.lastTurn = turn
		out = append(out, st.reminder)
	}
	if len(out) == 0 {
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ClearRun removes all reminder state for the given run.
func (e *Engine) ClearRun(runID string) {
	if runID == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.runs, runID)
}

func (e *Engine) ensureRunLocked(runID string) *runState {
	rs, ok := e.runs[runID]
	if ok && rs != nil {
		return rs
	}
	rs = &runState{
		reminders: make(map[string]*reminderState),
	}
	e.runs[runID] = rs
	return rs
}

// shouldEmit evaluates whether a reminder should be emitted on the given turn,
// based on its lifetime configuration and current state. TierSafety reminders
// are never suppressed by per-run caps, but MinTurnsBetween still applies to
// avoid pathological repetition.
func shouldEmit(st *reminderState, turn int) bool {
	if st == nil {
		return false
	}
	r := st.reminder
	// Enforce per-run cap for non-safety tiers.
	if r.MaxPerRun > 0 && st.emitted >= r.MaxPerRun && r.Priority != TierSafety {
		return false
	}
	// Enforce minimum turn spacing when configured.
	if r.MinTurnsBetween > 0 && st.lastTurn > 0 {
		if delta := turn - st.lastTurn; delta >= 0 && delta < r.MinTurnsBetween {
			return false
		}
	}
	return true
}
