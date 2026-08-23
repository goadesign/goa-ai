// Package outputcontract reports completed model or planner output that the
// agent runtime cannot accept. These failures end the run without asking a
// model for replacement output.
package outputcontract

type (
	// Origin identifies the component whose completed output failed validation.
	Origin string

	// Error reports completed model or planner output that broke its contract.
	Error struct {
		cause  error
		origin Origin
	}
)

const (
	// OriginModel identifies rejected model output.
	OriginModel Origin = "model"

	// OriginPlanner identifies a rejected planner result.
	OriginPlanner Origin = "planner"
)

// NewWithOrigin records why one known output boundary rejected a completed
// value.
func NewWithOrigin(cause error, origin Origin) *Error {
	if cause == nil {
		panic("outputcontract: error requires a cause")
	}
	if origin != OriginModel && origin != OriginPlanner {
		panic("outputcontract: error requires a valid origin")
	}
	return &Error{cause: cause, origin: origin}
}

// Error describes why the runtime rejected the completed output.
func (e *Error) Error() string {
	return "completed output does not meet its contract: " + e.cause.Error()
}

// Unwrap returns the original rejection error.
func (e *Error) Unwrap() error {
	return e.cause
}

// Origin identifies the component whose output failed validation.
func (e *Error) Origin() Origin {
	return e.origin
}
