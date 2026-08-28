// Package modelcall carries one model call's private phase results from the
// validated model client to the runtime activity that decides recovery.
package modelcall

import (
	"errors"
	"fmt"
	"reflect"
)

type (
	// Result records that one required operation ran and preserves its exact
	// returned error. A nil error still requires Called to be true.
	Result struct {
		Called bool
		Err    error
	}

	// Outcome records every provider and callback phase of one model call.
	// Model freezes this value after all prepared calls finish.
	Outcome struct {
		ProviderCall         Result
		ProviderReceives     []Result
		ProviderClose        Result
		Validations          []Result
		Completed            bool
		Incomplete           bool
		CompletionObservers  []Result
		StreamSetupObservers []Result
		ReceiveObservers     [][]Result
		CloseObservers       []Result
		Finishers            []Result
		Aborts               []Result
		Usage                []Result
		Staging              []Result
		Context              Result
		Framework            Result
		HasFinalizer         bool
		FinalizerIndex       int
	}

	// Finalizer receives one immutable outcome after every prepared Finish
	// callback has run. Runtime uses it to freeze activity-local journal state.
	Finalizer interface {
		FinalizeModelCall(Outcome) error
	}
)

// Clone returns an outcome whose slices can be retained independently.
func (o Outcome) Clone() Outcome {
	cloned := o
	cloned.ProviderReceives = append([]Result(nil), o.ProviderReceives...)
	cloned.Validations = append([]Result(nil), o.Validations...)
	cloned.CompletionObservers = append([]Result(nil), o.CompletionObservers...)
	cloned.StreamSetupObservers = append([]Result(nil), o.StreamSetupObservers...)
	cloned.CloseObservers = append([]Result(nil), o.CloseObservers...)
	cloned.Finishers = append([]Result(nil), o.Finishers...)
	cloned.Aborts = append([]Result(nil), o.Aborts...)
	cloned.Usage = append([]Result(nil), o.Usage...)
	cloned.Staging = append([]Result(nil), o.Staging...)
	cloned.ReceiveObservers = make([][]Result, len(o.ReceiveObservers))
	for i, results := range o.ReceiveObservers {
		cloned.ReceiveObservers[i] = append([]Result(nil), results...)
	}
	return cloned
}

// Error joins the recorded failures for outward diagnostics. Callers must use
// the structured fields, not this derived error, to decide whether recovery is
// safe.
func (o Outcome) Error() error {
	var failures []error
	failures = appendFailure(failures, o.ProviderCall.Err)
	failures = appendFailure(failures, resultErrors(o.ProviderReceives))
	failures = appendFailure(failures, o.ProviderClose.Err)
	failures = appendFailure(failures, resultErrors(o.Validations))
	if o.Incomplete {
		failures = append(failures, errors.New("model stream was not completely consumed"))
	}
	failures = appendFailure(failures, resultErrors(o.CompletionObservers))
	failures = appendFailure(failures, resultErrors(o.StreamSetupObservers))
	for _, results := range o.ReceiveObservers {
		failures = appendFailure(failures, resultErrors(results))
	}
	failures = appendFailure(failures, resultErrors(o.CloseObservers))
	failures = appendFailure(failures, resultErrors(o.Finishers))
	failures = appendFailure(failures, resultErrors(o.Aborts))
	failures = appendFailure(failures, resultErrors(o.Usage))
	failures = appendFailure(failures, resultErrors(o.Staging))
	failures = appendFailure(failures, o.Context.Err)
	failures = appendFailure(failures, o.Framework.Err)
	return joinFailures(failures)
}

// Exact reports whether left and right are the same error object or sentinel.
// Wrapping one error in another does not make the wrapper exact.
func Exact(left, right error) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return reflect.ValueOf(left) == reflect.ValueOf(right)
}

// ValidateFinalized verifies that all required callback slots were recorded
// before the outcome reached the runtime finalizer.
func (o Outcome) ValidateFinalized() error {
	if !o.ProviderCall.Called {
		return errors.New("model provider call outcome is missing")
	}
	if err := requireCalled("completion observer", o.CompletionObservers); err != nil {
		return err
	}
	if err := requireCalled("stream setup observer", o.StreamSetupObservers); err != nil {
		return err
	}
	for i, results := range o.ReceiveObservers {
		if err := requireCalled(fmt.Sprintf("receive observer set %d", i), results); err != nil {
			return err
		}
	}
	if err := requireCalled("close observer", o.CloseObservers); err != nil {
		return err
	}
	if err := requireCalled("prepared finisher", o.Finishers); err != nil {
		return err
	}
	return nil
}

func resultErrors(results []Result) error {
	var failures []error
	for _, result := range results {
		failures = appendFailure(failures, result.Err)
	}
	return joinFailures(failures)
}

func requireCalled(name string, results []Result) error {
	for i, result := range results {
		if !result.Called {
			return fmt.Errorf("%s %d outcome is missing", name, i)
		}
	}
	return nil
}

func appendFailure(failures []error, err error) []error {
	if err == nil {
		return failures
	}
	return append(failures, err)
}

func joinFailures(failures []error) error {
	switch len(failures) {
	case 0:
		return nil
	case 1:
		return failures[0]
	default:
		return errors.Join(failures...)
	}
}
