// Package anthropic wires Anthropic Claude model clients into goa-ai planners.
//
// The adapter adapts per-model capability differences rather than forwarding
// every field unconditionally: newer Claude generations (Opus 4.7+, Sonnet
// 5+, Fable/Mythos) reject the `temperature` sampling parameter outright, so
// the client omits it for those models instead of forwarding a value that is
// guaranteed to 400. See temperature.go for the capability rule.
package anthropic
