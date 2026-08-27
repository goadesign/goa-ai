// Package correction defines the shared size bound for framework-authored
// model correction guidance. Model validation produces the guidance and the
// workflow boundary enforces the same limit before scheduling a replacement.
package correction

// MaxBytes is the largest correction string that may cross a workflow activity
// boundary. The bound applies to one rejected model invocation or answer.
const MaxBytes = 4096
