package anthropic

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

// TestPrepareRequestOmitsTemperatureForUnsupportedModels is the adapter-level
// proof for the shared capability rule
// (features/model/internal/claudecaps.TemperatureSupported): a caller-configured,
// non-default temperature must never reach the wire for a model that rejects
// it, even though the field is set on both Options and the request. Before
// this fix, prepareRequest forwarded Request.Temperature unconditionally and
// a generic caller with a temperature knob (e.g. a downstream service
// defaulting to 0.2) would 400 on every request to claude-sonnet-5.
func TestPrepareRequestOmitsTemperatureForUnsupportedModels(t *testing.T) {
	unsupported := []string{
		"claude-sonnet-5",
		"claude-sonnet-5@20260201",
		"claude-sonnet-6",
		"claude-opus-4-7",
		"claude-opus-4-8",
		"claude-fable-5",
		"claude-mythos-5",
	}
	for _, modelID := range unsupported {
		t.Run(modelID, func(t *testing.T) {
			cl := &Client{
				defaultModel: modelID,
				maxTok:       4096,
			}

			params := completionParamsFor(t, cl, &model.Request{
				Temperature: 0.2,
				Messages: []*model.Message{{
					Role:  model.ConversationRoleUser,
					Parts: []model.Part{model.TextPart{Text: "hello"}},
				}},
			})
			assert.False(t, params.Temperature.Valid(), "temperature must be omitted for %s", modelID)
		})
	}
}

// TestPrepareRequestKeepsTemperatureForSupportedModels is the regression
// counterpart: models that still accept temperature must continue to receive
// it unchanged.
func TestPrepareRequestKeepsTemperatureForSupportedModels(t *testing.T) {
	supported := []string{
		"claude-opus-4-6",
		"claude-sonnet-4-6",
		"claude-sonnet-4-5-20250929",
		"claude-haiku-4-5-20251001",
		"claude-3-5-sonnet-20241022",
	}
	for _, modelID := range supported {
		t.Run(modelID, func(t *testing.T) {
			cl := &Client{
				defaultModel: modelID,
				maxTok:       4096,
			}

			params := completionParamsFor(t, cl, &model.Request{
				Temperature: 0.2,
				Messages: []*model.Message{{
					Role:  model.ConversationRoleUser,
					Parts: []model.Part{model.TextPart{Text: "hello"}},
				}},
			})
			require.True(t, params.Temperature.Valid(), "temperature must be sent for %s", modelID)
			assert.InDelta(t, 0.2, params.Temperature.Value, 0.0001)
		})
	}
}

// TestPrepareRequestOmitsZeroTemperatureRegardlessOfModel keeps the existing
// zero-value-means-unset behavior intact: a request that never set
// Temperature (and whose client has no configured default) must not send the
// field on any model, supported or not — this was already true before the
// capability fix and must remain so.
func TestPrepareRequestOmitsZeroTemperatureRegardlessOfModel(t *testing.T) {
	for _, modelID := range []string{"claude-opus-4-6", "claude-sonnet-5"} {
		t.Run(modelID, func(t *testing.T) {
			cl := &Client{
				defaultModel: modelID,
				maxTok:       4096,
			}

			params := completionParamsFor(t, cl, &model.Request{
				Messages: []*model.Message{{
					Role:  model.ConversationRoleUser,
					Parts: []model.Part{model.TextPart{Text: "hello"}},
				}},
			})
			assert.False(t, params.Temperature.Valid())
		})
	}
}
