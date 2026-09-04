package vertex

import (
	"goa.design/goa-ai/features/model/internal/modelid"
	"goa.design/goa-ai/runtime/agent/model"
	"google.golang.org/genai"
)

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
	// ThinkingBudget is the default thinking token budget for models that accept
	// numeric budgets. Gemini 3 uses thinking levels and does not read this
	// option.
	ThinkingBudget int
	// DisabledThinkingLevel maps ThinkingOptions{Enable: false} to the least
	// reasoning level accepted by every configured Gemini 3 model. Leave it
	// empty when those models cannot represent disabled thinking.
	DisabledThinkingLevel genai.ThinkingLevel
}

// geminiProviderName identifies the Gemini-on-Vertex adapter in provider errors.
const geminiProviderName = "vertex-gemini"

// resolveModelID returns the exact configured model for req.
func (o Options) resolveModelID(req *model.Request) (string, error) {
	return modelid.Resolve("vertex", req, o.DefaultModel, o.HighModel, o.SmallModel)
}
