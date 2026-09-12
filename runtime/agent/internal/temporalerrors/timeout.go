package temporalerrors

// Native Temporal timeouts already have a lossless SDK failure representation.
// Preserve only the known activity/child chain ending in a timeout without a
// previous failure. Application errors and joined failures keep their existing
// classification instead of exposing or discarding a deeper cause.

import "go.temporal.io/sdk/temporal"

// IsNativeTimeout reports a Temporal activity or child timeout whose complete
// cause chain can retain its meaning through the SDK failure converter. A saved
// application error, including an older generic timeout message, never qualifies.
func IsNativeTimeout(err error) bool {
	for err != nil {
		//nolint:errorlint // Only these exact SDK wrappers preserve timeout ownership.
		switch current := err.(type) {
		case *temporal.ActivityError:
			err = current.Unwrap()
		case *temporal.ChildWorkflowExecutionError:
			err = current.Unwrap()
		case *temporal.TimeoutError:
			return current.Unwrap() == nil
		default:
			return false
		}
	}
	return false
}
