// Package claudecaps is the single source of truth for Anthropic Claude
// model-capability rules shared by every adapter that speaks to Claude
// (features/model/anthropic — which also backs Claude-on-Vertex — and
// features/model/bedrock). Anthropic removes capabilities per model
// generation, not per endpoint, so the same rule must hold whether the model
// ID is a bare first-party alias ("claude-sonnet-5"), a dated snapshot
// ("claude-opus-4-7-20260315"), Vertex's "@" dated form
// ("claude-opus-4-7@20260315"), or a Bedrock inference-profile scope
// ("global.anthropic.claude-fable-5-v1:0"). All predicates therefore match
// on the stable "claude-<family>-<generation>" segment anywhere in the ID.
//
// Default direction for TemperatureSupported: a KNOWN family with a
// parseable NEWER generation is treated as rejecting temperature — this
// matches Anthropic's published direction of travel (sampling parameters are
// removed from every generation after the deprecation boundary, never
// reintroduced) and avoids sending requests guaranteed to 400. Unknown
// families and unparseable IDs are treated as supporting temperature: a
// wrong send fails loudly with a provider 400 the caller can see, while a
// wrong omit silently drops the caller's sampling configuration.
package claudecaps

import (
	"strconv"
	"strings"
)

// TemperatureSupported reports whether modelID accepts the `temperature`
// sampling parameter. Anthropic removed temperature/top_p/top_k from newer
// Claude generations: a request carrying a non-default value gets a 400
// invalid_request_error ("temperature is deprecated for this model").
// Adapters must omit the parameter for those models instead of forwarding a
// value that is guaranteed to fail; the model then runs at its own default
// sampling behavior.
//
// The rule, per Anthropic's published deprecation boundary:
//
//   - Claude Opus 4.7 and later (generation 4 with minor >= 7, or any Opus
//     generation >= 5) reject temperature. Opus <= 4.6 — including the
//     dated 4.0 ID "claude-opus-4-20250514", whose 8-digit date segment is
//     not a minor version — still accepts it.
//   - Claude Sonnet 5 and later (numeric generation >= 5) reject
//     temperature. Sonnet 4.x (4.5, 4.6, ...) and the legacy
//     "claude-3-5-sonnet-*" naming (generation before the family word, so
//     unparseable here) still accept it.
//   - Claude Haiku 5 and later reject temperature per the same
//     newer-generation default; Haiku 4.5 and older still accept it.
//   - The Fable/Mythos ("Claude 5") generation rejects temperature
//     unconditionally — thinking is always on and sampling parameters were
//     removed entirely. "claude-mythos-preview" predates the removal and
//     still accepts it.
//   - Unknown families and unparseable IDs accept temperature (see the
//     package doc for why the failure directions differ).
func TemperatureSupported(modelID string) bool {
	if IsFableGeneration(modelID) {
		return false
	}
	if gen, minor, hasMinor, ok := familyVersion(modelID, "claude-opus-"); ok {
		if gen >= 5 {
			return false
		}
		return gen != 4 || !hasMinor || minor < 7
	}
	if gen, _, _, ok := familyVersion(modelID, "claude-sonnet-"); ok {
		return gen < 5
	}
	if gen, _, _, ok := familyVersion(modelID, "claude-haiku-"); ok {
		return gen < 5
	}
	return true
}

// AdaptiveThinkingRequired reports whether modelID requires adaptive
// thinking configuration. Starting with Opus 4.6, Anthropic deprecates the
// manual type:"enabled" + budget_tokens config in favor of type:"adaptive",
// where the model dynamically decides when and how deeply to reason.
// Interleaved thinking is automatic in adaptive mode — no beta header is
// needed. On Opus 4.7+ the legacy config is removed entirely and returns a
// 400 error. On the Claude 5 generation (Fable/Mythos) thinking is always
// on; only type:"adaptive" is accepted and the legacy config likewise
// returns a 400 error.
func AdaptiveThinkingRequired(modelID string) bool {
	if IsFableGeneration(modelID) {
		return true
	}
	gen, minor, hasMinor, ok := familyVersion(modelID, "claude-opus-")
	if !ok {
		return false
	}
	return gen >= 5 || (gen == 4 && hasMinor && minor >= 6)
}

// IsFableGeneration reports whether modelID belongs to the Claude 5
// generation (Fable and its Mythos sibling), across every published naming
// shape: bare, dated snapshot, Vertex "@" dated, and Bedrock in-region, geo,
// and global scopes. The match requires a digit after the family word so
// "claude-mythos-preview" — which predates the generation and keeps the old
// capability surface — does not classify as Claude 5.
func IsFableGeneration(modelID string) bool {
	_, _, _, fable := familyVersion(modelID, "claude-fable-")
	_, _, _, mythos := familyVersion(modelID, "claude-mythos-")
	return fable || mythos
}

// familyVersion extracts the generation (and optional minor version) that
// follows marker in modelID, e.g. ("claude-opus-", "us.anthropic.claude-opus-4-7")
// yields (4, 7, true, true) and ("claude-sonnet-", "claude-sonnet-5@20260201")
// yields (5, 0, false, true). Version segments are at most two digits: a
// longer digit run is a date ("claude-opus-4-20250514" is dated Opus 4.0, not
// Opus 4.20250514) and terminates parsing, so the segment before it stands
// alone. ok is false when marker is absent or not followed by a version
// segment (legacy pre-generation naming such as "claude-3-5-sonnet-20241022"
// or "claude-mythos-preview").
func familyVersion(modelID, marker string) (gen, minor int, hasMinor, ok bool) {
	start := strings.Index(modelID, marker)
	if start < 0 {
		return 0, 0, false, false
	}
	rest := modelID[start+len(marker):]
	gen, rest, ok = takeVersionSegment(rest)
	if !ok {
		return 0, 0, false, false
	}
	if strings.HasPrefix(rest, "-") {
		if m, _, mok := takeVersionSegment(rest[1:]); mok {
			return gen, m, true, true
		}
	}
	return gen, 0, false, true
}

// takeVersionSegment parses a leading run of at most two digits as a version
// number and returns the remainder of the string after it. Runs longer than
// two digits are dates or snapshot stamps, not versions, so they do not
// parse.
func takeVersionSegment(s string) (n int, rest string, ok bool) {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 || end > 2 {
		return 0, s, false
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0, s, false
	}
	return n, s[end:], true
}
