package tools

// Ident is the strong type for fully qualified tool identifiers
// (e.g., "service.toolset.tool"). Use this type when referencing
// tools in maps or APIs to avoid accidental mixing with free-form strings.
type Ident string
