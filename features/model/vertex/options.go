package vertex

import "goa.design/goa-ai/runtime/agent/model"

// Options configures the Gemini-on-Vertex model client.
//
// Model IDs are Vertex publisher model names (e.g. "gemini-2.5-pro",
// "gemini-2.5-flash"). DefaultModel is required; HighModel and SmallModel
// are optional per-class overrides.
type Options struct {
	// DefaultModel is used when the request names no model and no class
	// override matches.
	DefaultModel string
	// HighModel serves ModelClassHighReasoning requests when set.
	HighModel string
	// SmallModel serves ModelClassSmall requests when set.
	SmallModel string
	// MaxTokens caps output tokens when the request does not set MaxTokens.
	MaxTokens int
	// Temperature is the default sampling temperature when the request does
	// not set one.
	Temperature float32
	// ThinkingBudget is the default thinking token budget applied when the
	// request enables thinking without a budget.
	ThinkingBudget int
}

// geminiProviderName identifies the Gemini-on-Vertex adapter in provider errors.
const geminiProviderName = "vertex-gemini"

// resolveModelID picks the concrete Vertex model for a request: an explicit
// Request.Model wins, then the configured class override, then DefaultModel.
func (o Options) resolveModelID(req *model.Request) string {
	if req.Model != "" {
		return req.Model
	}
	switch req.ModelClass { //nolint:exhaustive
	case model.ModelClassHighReasoning:
		if o.HighModel != "" {
			return o.HighModel
		}
	case model.ModelClassSmall:
		if o.SmallModel != "" {
			return o.SmallModel
		}
	}
	return o.DefaultModel
}
