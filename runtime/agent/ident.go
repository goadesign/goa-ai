// Package agent provides strong type identifiers for agents and tools.
package agent

// Ident is the strong type for fully qualified agent identifiers
// (e.g., "service.agent"). Use this type when referencing agents in
// maps or APIs to avoid accidental mixing with free-form strings.
type Ident string
