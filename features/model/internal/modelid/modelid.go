// Package modelid resolves logical model classes to exact provider model IDs.
// Adapters use it before request translation so a missing class configuration
// fails instead of silently running a different model.
package modelid

import (
	"fmt"

	"goa.design/goa-ai/runtime/agent/model"
)

// Resolve returns the concrete model selected by req. An explicit model wins;
// class-based requests require the matching configured model.
func Resolve(
	provider string,
	req *model.Request,
	defaultModel, highModel, smallModel string,
) (string, error) {
	if req.Model != "" {
		return req.Model, nil
	}
	switch req.ModelClass {
	case "", model.ModelClassDefault:
		if defaultModel == "" {
			return "", fmt.Errorf("%s: default model is not configured", provider)
		}
		return defaultModel, nil
	case model.ModelClassHighReasoning:
		if highModel == "" {
			return "", fmt.Errorf("%s: high-reasoning model class requested but HighModel is not configured", provider)
		}
		return highModel, nil
	case model.ModelClassSmall:
		if smallModel == "" {
			return "", fmt.Errorf("%s: small model class requested but SmallModel is not configured", provider)
		}
		return smallModel, nil
	default:
		return "", fmt.Errorf("%s: unsupported model class %q", provider, req.ModelClass)
	}
}
