// Package toolstats derives completion counts and telemetry without retaining
// tool results. Both live completion and saved legacy-result decoding use these
// checked sums so a resumed invocation and its retained result agree.
package toolstats

import (
	"errors"
	"math"

	"goa.design/goa-ai/runtime/agent/telemetry"
)

// Accumulator counts each tool result and combines its common telemetry fields.
// Failed attempts contribute statistics but do not determine terminal success.
type Accumulator struct {
	count    int
	tokens   int
	duration int64
	model    string
	mixed    bool
}

// Add includes one result. Nil telemetry still counts the result. Negative or
// overflowing statistics are rejected instead of wrapping into a valid total.
func (a *Accumulator) Add(t *telemetry.ToolTelemetry) error {
	if a.count == math.MaxInt {
		return errors.New("tool count overflows int")
	}
	if t != nil {
		if t.TokensUsed < 0 || t.TokensUsed > math.MaxInt-a.tokens {
			return errors.New("token telemetry overflows int")
		}
		if t.DurationMs < 0 || t.DurationMs > math.MaxInt64-a.duration {
			return errors.New("duration telemetry overflows int64")
		}
		a.tokens += t.TokensUsed
		a.duration += t.DurationMs
		if t.Model != "" {
			if a.model == "" {
				a.model = t.Model
			} else if a.model != t.Model {
				a.mixed = true
			}
		}
	}
	a.count++
	return nil
}

// Result returns the count and a fresh common-field summary. No telemetry is
// returned when every result had zero totals and no model name.
func (a *Accumulator) Result() (int, *telemetry.ToolTelemetry) {
	if a.tokens == 0 && a.duration == 0 && a.model == "" {
		return a.count, nil
	}
	t := &telemetry.ToolTelemetry{TokensUsed: a.tokens, DurationMs: a.duration}
	if !a.mixed {
		t.Model = a.model
	}
	return a.count, t
}
