// Package toolregistry validates the saved operation at registry admission and
// provider delivery. Sequence zero is the original call; every new operation
// has a continuation and a new sequence, while duplicate delivery keeps both.
package toolregistry

import (
	"fmt"

	"goa.design/goa-ai/runtime/agent/api"
)

// ValidateExecution checks operation identity and the selected value before an
// immutable request is admitted. Text-only calls may query or cancel Tasks but
// cannot carry host answers or continue an input exchange.
func ValidateExecution(sequence uint64, continuation *api.ExecutionContinuation, textOnly bool) error {
	if (sequence == 0) != (continuation == nil) {
		return fmt.Errorf("execution continuation must be absent at sequence zero and present on later operations")
	}
	if continuation == nil {
		return nil
	}
	if err := continuation.Validate(); err != nil {
		return err
	}
	if textOnly {
		_, input := continuation.AsInput()
		_, _, update := continuation.AsTaskUpdate()
		if input || update {
			return fmt.Errorf("text-only calls cannot carry host input")
		}
	}
	return nil
}
