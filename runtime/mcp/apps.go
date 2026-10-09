// Package mcp checks Apps tool declarations received from a remote server before
// a model caller executes the tool. Missing UI metadata permits both callers;
// explicit app-only visibility prevents execution through the model caller.
package mcp

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// toolModelVisibility validates the current nested UI declaration and returns
// whether the model may invoke the tool. Unknown extension fields do not change
// that decision, and absent visibility uses the Apps default of model and app.
func toolModelVisibility(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 {
		return true, nil
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
		return false, errors.New("tool metadata must be an object")
	}
	ui, present := metadata["ui"]
	if !present {
		return true, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(ui, &fields); err != nil || fields == nil {
		return false, errors.New("tool UI metadata must be an object")
	}
	if rawURI, present := fields["resourceUri"]; present {
		var uri string
		if err := json.Unmarshal(rawURI, &uri); err != nil || !strings.HasPrefix(uri, "ui://") {
			return false, errors.New("tool UI resourceUri must be a ui:// address")
		}
		parsed, err := url.Parse(uri)
		if err != nil || (parsed.Host == "" && parsed.Path == "") {
			return false, errors.New("tool UI resourceUri must be a ui:// address")
		}
	}
	visibility, present := fields["visibility"]
	if !present {
		return true, nil
	}
	var callers []string
	if err := json.Unmarshal(visibility, &callers); err != nil || len(callers) == 0 {
		return false, errors.New("tool UI visibility must contain model, app, or both")
	}
	model, app := false, false
	for _, caller := range callers {
		switch caller {
		case "model":
			if model {
				return false, errors.New("tool UI visibility repeats model")
			}
			model = true
		case "app":
			if app {
				return false, errors.New("tool UI visibility repeats app")
			}
			app = true
		default:
			return false, errors.New("tool UI visibility must contain model, app, or both")
		}
	}
	return model, nil
}
