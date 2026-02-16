package tools

import "strings"

// ServerDataMode controls whether optional server-data is produced for a tool call.
//
// The runtime reads the mode from the reserved `server_data` field in tool payload
// JSON. The field is stripped before decoding into the Go payload type so that
// tool payload structs do not need to carry the toggle field.
//
// Valid values are "auto", "on", and "off". The zero value means the caller did
// not specify a mode.
type ServerDataMode string

const (
	// ServerDataModeAuto lets the runtime choose whether to emit optional server-data.
	ServerDataModeAuto ServerDataMode = "auto"

	// ServerDataModeOn forces optional server-data to be produced when the tool supports it.
	ServerDataModeOn ServerDataMode = "on"

	// ServerDataModeOff disables optional server-data production for the tool call.
	ServerDataModeOff ServerDataMode = "off"
)

// ParseServerDataMode normalizes s to a ServerDataMode.
// It returns the zero value when s is not a recognized mode.
func ParseServerDataMode(s string) ServerDataMode {
	switch strings.ToLower(s) {
	case string(ServerDataModeAuto):
		return ServerDataModeAuto
	case string(ServerDataModeOn):
		return ServerDataModeOn
	case string(ServerDataModeOff):
		return ServerDataModeOff
	default:
		return ""
	}
}

// Valid reports whether m is a recognized non-zero server-data mode.
func (m ServerDataMode) Valid() bool {
	switch m {
	case ServerDataModeAuto, ServerDataModeOn, ServerDataModeOff:
		return true
	default:
		return false
	}
}

// OptionalServerDataEnabled reports whether optional server-data should be emitted or
// decoded for a tool call.
//
// Contract:
//   - mode is the per-call toggle selected by the caller via the reserved `server_data`
//     payload field.
//   - defaultOn reflects the tool's design-time default (ServerDataDefault == "" or "on").
func OptionalServerDataEnabled(mode ServerDataMode, defaultOn bool) bool {
	switch mode {
	case ServerDataModeOn:
		return true
	case ServerDataModeOff:
		return false
	case ServerDataModeAuto, "":
		return defaultOn
	default:
		return defaultOn
	}
}
