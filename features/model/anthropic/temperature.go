// This file: model-capability rule for the Anthropic Messages `temperature`
// sampling parameter. Anthropic removed `temperature`/`top_p`/`top_k` from
// newer Claude generations rather than deprecating them softly: a request
// that carries a non-default value gets a 400 invalid_request_error
// ("temperature is deprecated for this model") — live-verified against
// claude-sonnet-5 on Vertex, which shares this contract with Claude Opus 4.7
// and later and with the Fable/Mythos ("Claude 5") family. Adjacent-layer
// contract: prepareRequest (client.go) calls temperatureSupported before
// setting params.Temperature and omits the field entirely for unsupported
// models instead of forwarding a value that is guaranteed to fail — the
// model runs at its own default sampling behavior. This mirrors the
// equivalent capability table already maintained for Claude on Bedrock
// (features/model/bedrock's supportsTemperature); keep both in sync if
// Anthropic changes the deprecation boundary.

package anthropic

import (
	"context"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// temperatureSupported reports whether modelID accepts the Anthropic
// Messages API's `temperature` sampling parameter. The rule matches by model
// family and generation, not an exhaustive ID list, so dated snapshots and
// region-qualified variants of an already-affected family classify correctly
// without a table update:
//
//   - Claude Opus 4.7 and later (opus generation "4" with minor version >= 7)
//     reject temperature.
//   - Claude Sonnet 5 and later reject temperature. Sonnet 4.x (4.5, 4.6, ...)
//     is unaffected.
//   - The Claude 5 generation (Fable 5, Mythos 5) rejects temperature
//     unconditionally — thinking is always on and sampling parameters were
//     removed entirely.
//   - Haiku and every other model family still accept temperature normally.
//
// A brand-new family, or a generation boundary Anthropic has not yet
// published (e.g. a hypothetical Haiku 5), requires a source update here,
// same as any other adapter capability table — this function intentionally
// does not guess at unannounced deprecations.
func temperatureSupported(modelID string) bool {
	if isFableGenerationModel(modelID) || isSonnet5OrLaterModel(modelID) {
		return false
	}
	minor, ok := opusMinorVersion(modelID)
	return !ok || minor < 7
}

// isFableGenerationModel reports whether modelID belongs to the Claude 5
// generation (Fable and its Mythos sibling). Matching on the stable
// "claude-fable-5"/"claude-mythos-5" segment covers bare IDs, dated
// snapshots ("claude-fable-5-20260315"), and Vertex's "@" dated form
// ("claude-fable-5@20260315") without enumerating each suffix.
func isFableGenerationModel(modelID string) bool {
	return strings.Contains(modelID, "claude-fable-5") || strings.Contains(modelID, "claude-mythos-5")
}

// isSonnet5OrLaterModel reports whether modelID is Claude Sonnet 5 or a dated
// snapshot of it. Sonnet's next-generation naming drops the "4-" minor
// segment used by Sonnet 4.5/4.6 ("claude-sonnet-5" vs "claude-sonnet-4-6"),
// so a substring match on "claude-sonnet-5" cannot collide with the still-
// supported 4.x line.
func isSonnet5OrLaterModel(modelID string) bool {
	return strings.Contains(modelID, "claude-sonnet-5")
}

// traceTemperatureOmitted marks the ambient span (if any — trace.SpanFromContext
// returns a no-op span when ctx carries none) with the fact that a caller-
// configured, non-default temperature was dropped from the wire request
// because modelID does not support it. This is the only signal a caller gets
// that their Options.Temperature / Request.Temperature had no effect, so it
// must survive independently of whether the call ultimately succeeds or
// fails.
func traceTemperatureOmitted(ctx context.Context, modelID string, requested float64) {
	trace.SpanFromContext(ctx).SetAttributes(
		attribute.Bool("gen_ai.anthropic.temperature_omitted", true),
		attribute.String("gen_ai.anthropic.temperature_omitted_model", modelID),
		attribute.Float64("gen_ai.anthropic.temperature_requested", requested),
	)
}

// opusMinorVersion extracts the minor number from an Opus 4 model ID (e.g. 7
// from "claude-opus-4-7", "claude-opus-4-7-20260315", or the Vertex dated
// form "claude-opus-4-7@20260315"). ok is false when modelID has no
// "claude-opus-4-<digits>" segment, e.g. Opus 3, or a non-Opus family.
func opusMinorVersion(modelID string) (minor int, ok bool) {
	const marker = "claude-opus-4-"
	start := strings.Index(modelID, marker)
	if start < 0 {
		return 0, false
	}
	rest := modelID[start+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	minor, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0, false
	}
	return minor, true
}
