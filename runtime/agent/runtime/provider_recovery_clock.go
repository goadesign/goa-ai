package runtime

// Provider recovery clock credit is separate from the root's allowance debit.
// An ancestor receives credit only while it actually waits and every unfinished
// branch is known to be blocked by recovery. Healthy work, unknown outcomes,
// and local publication work continue to consume active time.

import "time"

type (
	// providerRecoveryClock keeps measured evidence for one workflow execution.
	// It never treats a future permission window as elapsed recovery time.
	providerRecoveryClock struct {
		own      providerRecoveryIntervals
		waited   providerRecoveryIntervals
		branches []*providerRecoveryBranch
		credited providerRecoveryIntervals
		waitFrom time.Time
		waiting  bool
		closed   bool
	}

	// providerRecoveryBranch belongs to an actually scheduled activity or child.
	// A child's paused intervals describe that child's whole unfinished subtree,
	// not merely one failing descendant. Service activities never report pauses.
	providerRecoveryBranch struct {
		start  time.Time
		end    time.Time
		paused providerRecoveryIntervals
	}
)

// branch begins tracking accepted work. Readiness observed through the existing
// future closes its lifetime; healthy work needs no recovery control message.
func (c *providerRecoveryClock) branch(start time.Time) *providerRecoveryBranch {
	branch := &providerRecoveryBranch{start: start}
	c.branches = append(c.branches, branch)
	return branch
}

// beginWait records when the workflow actually stops doing local work to wait.
// Dispatch, result publication, and other local work remain outside this span.
func (c *providerRecoveryClock) beginWait(now time.Time) {
	c.waitFrom = now
	c.waiting = true
}

// endWait closes a real workflow wait before the caller resumes local work.
func (c *providerRecoveryClock) endWait(now time.Time) {
	c.waited = c.waited.add(providerRecoveryInterval{start: c.waitFrom, end: now})
	c.waiting = false
}

// observe recomputes measured credit through now and returns only newly covered
// time. Unresolved branches remain active. After a terminal decision, retained
// late evidence cannot move the workflow's deadlines or reopen its work.
func (c *providerRecoveryClock) observe(now time.Time) time.Duration {
	if c.closed {
		return 0
	}
	before := c.credited.duration()
	waited := c.waited
	if c.waiting {
		waited = waited.add(providerRecoveryInterval{start: c.waitFrom, end: now})
	}
	paused := waited
	var live providerRecoveryIntervals
	for _, branch := range c.branches {
		end := now
		if !branch.end.IsZero() && branch.end.Before(end) {
			end = branch.end
		}
		lifetime := providerRecoveryIntervals{}.add(providerRecoveryInterval{
			start: branch.start,
			end:   end,
		})
		live = live.add(providerRecoveryInterval{start: branch.start, end: end})
		paused = paused.subtract(lifetime.subtract(branch.paused))
	}
	paused = paused.intersect(live)
	for _, interval := range c.own {
		c.credited = c.credited.add(interval)
	}
	for _, interval := range paused {
		c.credited = c.credited.add(interval)
	}
	return c.credited.duration() - before
}
