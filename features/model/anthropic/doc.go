// Package anthropic wires Anthropic Claude model clients into goa-ai planners.
//
// The adapter adapts per-model capability differences rather than forwarding
// every field unconditionally: newer Claude generations (Opus 4.7+, Sonnet
// 5+, Fable/Mythos) reject the `temperature` sampling parameter outright, so
// the client omits it for those models instead of forwarding a value that is
// guaranteed to 400. The capability rule is shared with the Bedrock adapter
// via features/model/internal/claudecaps; temperature.go records the omission
// on the ambient trace span.
package anthropic
