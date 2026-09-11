// Package tooloutput exposes failed runs' final causes separately from earlier
// observations retained for troubleshooting. Applications must not infer the
// final cause from diagnostic ordering.
package tooloutput

// RunError is returned by a failed Run. It retains the exact error that ended
// the call and the complete frozen diagnostic history. Earlier rejected model
// responses remain available to errors.Is and errors.As, but do not become the
// final cause of a later provider or internal failure.
type RunError struct {
	terminal    error
	diagnostics error
}

// Error returns the complete diagnostic text, including earlier observations.
func (e *RunError) Error() string {
	return e.diagnostics.Error()
}

// Unwrap preserves inspection of all original terminal and observed errors.
func (e *RunError) Unwrap() error {
	return e.diagnostics
}

// TerminalError returns the exact final error before Run attached its diagnostic
// history. It may itself wrap or join multiple causes; callers must not assume
// that one matching descendant classifies every cause of the final failure.
func (e *RunError) TerminalError() error {
	return e.terminal
}
