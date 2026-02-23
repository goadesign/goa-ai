package prompt

// Ident is the strong type for globally unique prompt identifiers.
//
// Prompt IDs are canonical strings (for example, "example.agent.system") used
// across prompt registration, rendering, override storage, and observability.
// Using Ident avoids accidental mixing with unrelated string IDs such as run IDs
// or session IDs.
type Ident string

// String returns the raw string form of the prompt identifier.
func (id Ident) String() string {
	return string(id)
}
