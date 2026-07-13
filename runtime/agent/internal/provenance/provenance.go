// Package provenance defines process-local model invocation identity for the
// runtime journal. Tokens never cross planner or workflow boundaries.
package provenance

import "sync/atomic"

// Token identifies one model response within a planner activity.
type Token struct {
	id uint64
}

var next atomic.Uint64

// New returns a process-unique non-zero response token.
func New() Token {
	return Token{id: next.Add(1)}
}

// IsZero reports whether t carries no response identity.
func (t Token) IsZero() bool {
	return t.id == 0
}
