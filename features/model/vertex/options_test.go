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
		name string
		req  *model.Request
		want string
	}{
		{"explicit model wins", &model.Request{Model: "gemini-exp", ModelClass: model.ModelClassSmall}, "gemini-exp"},
		{"high class", &model.Request{ModelClass: model.ModelClassHighReasoning}, "gemini-2.5-pro-high"},
		{"small class", &model.Request{ModelClass: model.ModelClassSmall}, "gemini-2.5-flash"},
		{"default class", &model.Request{ModelClass: model.ModelClassDefault}, "gemini-2.5-pro"},
		{"unknown class falls back to default", &model.Request{ModelClass: model.ModelClass("weird")}, "gemini-2.5-pro"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, opts.resolveModelID(tc.req))
		})
	}
}

func TestResolveModelIDMissingClassFallsBack(t *testing.T) {
	opts := Options{DefaultModel: "gemini-2.5-pro"}
	assert.Equal(t, "gemini-2.5-pro",
		opts.resolveModelID(&model.Request{ModelClass: model.ModelClassHighReasoning}))
}
