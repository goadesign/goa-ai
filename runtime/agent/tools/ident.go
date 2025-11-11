package tools

// Ident is the strong type for globally unique tool identifiers.
// Tool IDs are simple DSL-declared names validated to be unique across the
// entire design. Use this type in maps and APIs to avoid mixing with free-form
// strings and to document intent at call sites.
type Ident string
