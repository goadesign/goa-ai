package runtime

// Provider recovery accounts for completed failed planner activities and entered
// backoffs once, even when they overlap. Outstanding permissions reserve capacity
// without spending it. Workflow history owns these values; only the settled
// remaining allowance belongs in a continuation checkpoint.

import (
	"slices"
	"time"
)

type (
	// providerRecoveryBudget retains only Remaining in external-input
	// checkpoints. Live accounting starts from that amount, never from the
	// original policy maximum. The workflow actor owns active-clock credit.
	providerRecoveryBudget struct {
		// Remaining is the confirmed allowance saved when the run suspends.
		Remaining time.Duration
		allowance *providerRecoveryAllowance
	}

	// providerRecoveryInterval uses a half-open interval: start is included and
	// end is excluded. Empty intervals carry no elapsed time.
	providerRecoveryInterval struct {
		start time.Time
		end   time.Time
	}

	// providerRecoveryIntervals stores ordered, disjoint intervals. Adjacent
	// intervals are merged, so duration counts every covered instant once.
	providerRecoveryIntervals []providerRecoveryInterval

	// providerRecoveryAllowance is the one owner's accounting state. initial
	// is the amount admitted at workflow entry, including a restored remainder.
	// confirmed contains actual failed activities and actual timer observations.
	// holds contain issued phases whose unused capacity is not yet proven.
	providerRecoveryAllowance struct {
		initial   time.Duration
		confirmed providerRecoveryIntervals
		holds     []*providerRecoveryReservation
		exhausted bool
	}

	// providerRecoveryReservation retains one original permission interval.
	// A late failure can prevent more grants but cannot extend this interval.
	providerRecoveryReservation struct {
		interval providerRecoveryInterval
	}
)

// activate reconstructs the live owner from its saved remainder. Continuing a
// suspended run starts a new interval history without refilling the allowance.
func (b *providerRecoveryBudget) activate() {
	if b.allowance == nil {
		b.allowance = &providerRecoveryAllowance{
			initial:   b.Remaining,
			exhausted: b.Remaining == 0,
		}
	}
}

// remaining reports confirmed spending only. Outstanding permissions are not
// exhaustion, and an unreserved initial failure may honestly exceed initial.
func (a *providerRecoveryAllowance) remaining() time.Duration {
	return max(0, a.initial-a.confirmed.duration())
}

// confirm records a measured interval in full, including a late first failure
// or timer overshoot. Once confirmed spending exhausts the allowance, later
// settlements cannot reopen admission.
func (a *providerRecoveryAllowance) confirm(interval providerRecoveryInterval) {
	a.confirmed = a.confirmed.add(interval)
	if a.remaining() == 0 {
		a.exhausted = true
	}
}

// reserve holds the largest affordable interval beginning now, clipped by the
// phase's existing limit when present. A nil result means admission must wait
// or terminate under its existing deadline; it does not mean confirmed spending
// exhausted the allowance. Already covered time can be shared at no extra cost.
func (a *providerRecoveryAllowance) reserve(now, limit time.Time) *providerRecoveryReservation {
	if a.exhausted {
		return nil
	}
	coverage := slices.Clone(a.confirmed)
	for _, hold := range a.holds {
		coverage = coverage.add(hold.interval)
	}
	used := coverage.duration()
	if used > a.initial {
		// Late actual evidence can overcommit outstanding holds. Their original
		// expiries remain fixed; a settlement must free capacity before admission.
		return nil
	}
	uncovered := a.initial - used
	end := now
	for _, interval := range coverage {
		if !interval.end.After(end) {
			continue
		}
		if interval.start.After(end) {
			gap := interval.start.Sub(end)
			if gap > uncovered {
				break
			}
			uncovered -= gap
		}
		end = interval.end
	}
	end = end.Add(uncovered)
	if !limit.IsZero() && limit.Before(end) {
		end = limit
	}
	if !end.After(now) {
		return nil
	}
	hold := &providerRecoveryReservation{
		interval: providerRecoveryInterval{start: now, end: end},
	}
	a.holds = append(a.holds, hold)
	return hold
}

// release removes a hold only after the runtime proves phase completion or
// nonactivation. Expiry and an ambiguous activity result do not call release.
func (a *providerRecoveryAllowance) release(hold *providerRecoveryReservation) {
	for i, candidate := range a.holds {
		if candidate == hold {
			a.holds = slices.Delete(a.holds, i, i+1)
			return
		}
	}
	panic("provider recovery released an unknown reservation")
}

// add merges actual coverage without mutating an earlier slice. Replayed
// observations and overlapping requests therefore earn no duplicate debit.
func (intervals providerRecoveryIntervals) add(next providerRecoveryInterval) providerRecoveryIntervals {
	if !next.end.After(next.start) {
		return intervals
	}
	result := make(providerRecoveryIntervals, 0, len(intervals)+1)
	inserted := false
	for _, current := range intervals {
		switch {
		case current.end.Before(next.start):
			result = append(result, current)
		case next.end.Before(current.start):
			if !inserted {
				result = append(result, next)
				inserted = true
			}
			result = append(result, current)
		default:
			if current.start.Before(next.start) {
				next.start = current.start
			}
			if current.end.After(next.end) {
				next.end = current.end
			}
		}
	}
	if !inserted {
		result = append(result, next)
	}
	return result
}

// duration returns elapsed union time, saturating at the largest duration so
// that late evidence can never overflow into a new positive allowance.
func (intervals providerRecoveryIntervals) duration() time.Duration {
	const largestDuration = time.Duration(1<<63 - 1)
	var total time.Duration
	for _, interval := range intervals {
		elapsed := interval.end.Sub(interval.start)
		if elapsed > largestDuration-total {
			return largestDuration
		}
		total += elapsed
	}
	return total
}

// intersect keeps only time covered by both sets. Ancestor clock credit uses
// this to require measured recovery time on every unfinished branch.
func (intervals providerRecoveryIntervals) intersect(other providerRecoveryIntervals) providerRecoveryIntervals {
	var result providerRecoveryIntervals
	i, j := 0, 0
	for i < len(intervals) && j < len(other) {
		start, end := intervals[i].start, intervals[i].end
		if other[j].start.After(start) {
			start = other[j].start
		}
		if other[j].end.Before(end) {
			end = other[j].end
		}
		result = result.add(providerRecoveryInterval{start: start, end: end})
		if intervals[i].end.Before(other[j].end) {
			i++
		} else {
			j++
		}
	}
	return result
}

// subtract removes known active time from candidate ancestor credit. A healthy
// or unresolved branch contributes its entire observed lifetime as active time.
func (intervals providerRecoveryIntervals) subtract(other providerRecoveryIntervals) providerRecoveryIntervals {
	var result providerRecoveryIntervals
	for _, interval := range intervals {
		start := interval.start
		for _, removed := range other {
			if !removed.end.After(start) {
				continue
			}
			if !removed.start.Before(interval.end) {
				break
			}
			if removed.start.After(start) {
				result = result.add(providerRecoveryInterval{start: start, end: removed.start})
			}
			if removed.end.After(start) {
				start = removed.end
			}
			if !start.Before(interval.end) {
				break
			}
		}
		result = result.add(providerRecoveryInterval{start: start, end: interval.end})
	}
	return result
}
