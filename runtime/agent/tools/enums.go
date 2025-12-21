package tools

import "strings"

// ArtifactsMode controls whether UI artifacts are produced for a tool call.
//
// The runtime reads the mode from the reserved `artifacts` field in tool payload
// JSON. The field is stripped before decoding into the Go payload type so that
// tool payload structs do not need to carry an artifacts toggle field.
//
// Valid values are "auto", "on", and "off". The zero value means the caller did
// not specify a mode.
type ArtifactsMode string

const (
	// ArtifactsModeAuto lets the runtime choose whether to emit artifacts.
	ArtifactsModeAuto ArtifactsMode = "auto"

	// ArtifactsModeOn forces artifacts to be produced when the tool supports them.
	ArtifactsModeOn ArtifactsMode = "on"

	// ArtifactsModeOff disables artifact production for the tool call.
	ArtifactsModeOff ArtifactsMode = "off"
)

// ParseArtifactsMode normalizes s to an ArtifactsMode.
// It returns the zero value when s is not a recognized mode.
func ParseArtifactsMode(s string) ArtifactsMode {
	switch strings.ToLower(s) {
	case string(ArtifactsModeAuto):
		return ArtifactsModeAuto
	case string(ArtifactsModeOn):
		return ArtifactsModeOn
	case string(ArtifactsModeOff):
		return ArtifactsModeOff
	default:
		return ""
	}
}

// Valid reports whether m is a recognized non-zero artifacts mode.
func (m ArtifactsMode) Valid() bool {
	switch m {
	case ArtifactsModeAuto, ArtifactsModeOn, ArtifactsModeOff:
		return true
	default:
		return false
	}
}
