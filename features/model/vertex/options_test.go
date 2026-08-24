package vertex

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestResolveModelID(t *testing.T) {
	opts := Options{
		DefaultModel: "gemini-2.5-pro",
		HighModel:    "gemini-2.5-pro-high",
		SmallModel:   "gemini-2.5-flash",
	}
	cases := []struct {
		name    string
		req     *model.Request
		want    string
		wantErr string
	}{
		{"explicit model wins", &model.Request{Model: "gemini-exp"}, "gemini-exp", ""},
		{"high class", &model.Request{ModelClass: model.ModelClassHighReasoning}, "gemini-2.5-pro-high", ""},
		{"small class", &model.Request{ModelClass: model.ModelClassSmall}, "gemini-2.5-flash", ""},
		{"default class", &model.Request{ModelClass: model.ModelClassDefault}, "gemini-2.5-pro", ""},
		{"unknown class is rejected", &model.Request{ModelClass: model.ModelClass("weird")}, "", `unsupported model class "weird"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := opts.resolveModelID(tc.req)
			assert.Equal(t, tc.want, got)
			if tc.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tc.wantErr)
			}
		})
	}
}

func TestResolveModelIDMissingClassIsRejected(t *testing.T) {
	opts := Options{DefaultModel: "gemini-2.5-pro"}
	got, err := opts.resolveModelID(&model.Request{ModelClass: model.ModelClassHighReasoning})
	assert.Empty(t, got)
	assert.EqualError(t, err, "vertex: high-reasoning model class requested but HighModel is not configured")
}
