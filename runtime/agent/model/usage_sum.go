// Package model owns checked token arithmetic shared by provider adapters and
// runtime accounting. Attribution belongs to the caller because combined
// counts can represent several models.
package model

import "fmt"

// AddTokenUsage adds validated counts without integer overflow. The returned
// value has no model attribution; callers attach an identity only when every
// count belongs to that model.
func AddTokenUsage(current, delta TokenUsage) (TokenUsage, error) {
	if err := validateTokenUsage(current); err != nil {
		return TokenUsage{}, err
	}
	if err := validateTokenUsage(delta); err != nil {
		return TokenUsage{}, err
	}
	sum := TokenUsage{
		InputTokens:      current.InputTokens + delta.InputTokens,
		OutputTokens:     current.OutputTokens + delta.OutputTokens,
		TotalTokens:      current.TotalTokens + delta.TotalTokens,
		CacheReadTokens:  current.CacheReadTokens + delta.CacheReadTokens,
		CacheWriteTokens: current.CacheWriteTokens + delta.CacheWriteTokens,
	}
	var overflow string
	switch {
	case sum.InputTokens < current.InputTokens:
		overflow = "input"
	case sum.OutputTokens < current.OutputTokens:
		overflow = "output"
	case sum.TotalTokens < current.TotalTokens:
		overflow = "total"
	case sum.CacheReadTokens < current.CacheReadTokens:
		overflow = "cache read"
	case sum.CacheWriteTokens < current.CacheWriteTokens:
		overflow = "cache write"
	}
	if overflow != "" {
		return TokenUsage{}, fmt.Errorf("%s token usage exceeds the supported integer range", overflow)
	}
	return sum, nil
}
