// Package mcp validates host interactions requested by an unfinished MCP call.
// It accepts the form and URL modes the host explicitly supports, checks each
// answer against its exact server request, and never interprets requestState.
package mcp

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/url"

	"goa.design/goa-ai/internal/jsonschema"
)

type (
	hostInputDisabledKey struct{}

	// InputSupport declares interactions the application can present and answer.
	// The caller advertises only these modes on each request.
	InputSupport struct {
		// Form enables non-sensitive, schema-validated form input.
		Form bool
		// URL enables explicit consent to an external interaction.
		URL bool
	}
	// CallContinuation contains the prior round's state and host answers.
	// It belongs to one unfinished invocation, never to a session or model prompt.
	CallContinuation struct {
		// RequestState is echoed byte-for-byte as the decoded server string.
		RequestState *string `json:"requestState,omitempty"` //nolint:tagliatelle // MCP defines this wire field name.
		// InputResponses maps exact server request IDs to their result objects.
		InputResponses map[string]json.RawMessage `json:"inputResponses,omitzero"` //nolint:tagliatelle // MCP defines this wire field name.
	}
	// elicitationParams retains the closed form-or-URL interaction received from a server.
	elicitationParams struct {
		Mode            string          `json:"mode"`
		Message         *string         `json:"message"`
		RequestedSchema json.RawMessage `json:"requestedSchema"` //nolint:tagliatelle // MCP defines this wire field name.
		URL             *string         `json:"url"`
	}
	elicitationResult struct {
		Action  string          `json:"action"`
		Content json.RawMessage `json:"content"`
	}
)

// WithoutHostInput prevents one operation and its child contexts from requesting
// further host input. It omits form and URL capabilities, rejects continuation
// data before sending a tool call, and rejects unfinished results, including
// state-only results. Other operations on the same caller retain their support.
func WithoutHostInput(ctx context.Context) context.Context {
	return context.WithValue(ctx, hostInputDisabledKey{}, struct{}{})
}

// Validate checks that an unfinished result can be fulfilled by this host.
func (r *InputRequired) Validate(support InputSupport) error {
	if r.Requests == nil && r.RequestState == nil {
		return errors.New("input_required needs inputRequests or requestState")
	}
	for id, request := range r.Requests {
		params, err := decodeElicitation(request)
		if err != nil {
			return fmt.Errorf("input request %q: %w", id, err)
		}
		if params.Mode == elicitationForm && !support.Form || params.Mode == "url" && !support.URL {
			return fmt.Errorf("input request %q uses an unadvertised %s capability", id, params.Mode)
		}
	}
	return nil
}

// ValidateResponses requires exactly the requested IDs and checks each accepted
// form against its schema. URL consent, decline and cancel carry no form data.
func (r *InputRequired) ValidateResponses(responses map[string]json.RawMessage) error {
	if len(responses) != len(r.Requests) {
		return errors.New("input responses do not match requested IDs")
	}
	for id, request := range r.Requests {
		raw, ok := responses[id]
		if !ok {
			return fmt.Errorf("missing input response %q", id)
		}
		if err := request.ValidateResponse(raw); err != nil {
			return fmt.Errorf("input response %q: %w", id, err)
		}
	}
	return nil
}

// ValidateFormResponse checks the action and flat content of one form answer
// before typed decoding can drop extra fields. Generated Goa validation checks
// declared field constraints after this check; decline and cancel have no content.
func ValidateFormResponse(response json.RawMessage) error {
	_, err := validateElicitationResult(elicitationForm, response)
	return err
}

// ValidateURLResponse checks one URL-consent answer. Accept, decline and cancel
// are the only actions, and none can carry form content. Extension fields remain
// allowed because MCP result objects may contain extension values.
func ValidateURLResponse(response json.RawMessage) error {
	_, err := validateElicitationResult("url", response)
	return err
}

// ValidateResponse checks one host answer against this exact input request.
// The consumer and generated server use the same form and URL result contract.
func (r InputRequest) ValidateResponse(response json.RawMessage) error {
	params, err := decodeElicitation(r)
	if err != nil {
		return err
	}
	return validateElicitationResponse(params, response)
}

// validateElicitationResponse checks an external server's answer shape and then
// validates accepted content against that server's exact requested schema.
func validateElicitationResponse(params elicitationParams, raw json.RawMessage) error {
	response, err := validateElicitationResult(params.Mode, raw)
	if err != nil {
		return err
	}
	if params.Mode == elicitationForm && response.Action == "accept" {
		compiled, err := jsonschema.Compile(params.RequestedSchema)
		if err != nil {
			return err
		}
		if err := jsonschema.Validate(compiled, response.Content); err != nil {
			return fmt.Errorf("form response: %w", err)
		}
	}
	return nil
}

// validateElicitationResult checks actions and content before any typed decoder
// can ignore extension fields. Unknown flat form values are allowed; nested
// values fail. The caller receives the checked content for its own field rules.
func validateElicitationResult(mode string, raw json.RawMessage) (elicitationResult, error) {
	var response elicitationResult
	if err := jsonv2.Unmarshal(raw, &response); err != nil {
		return response, fmt.Errorf("input response: %w", err)
	}
	switch response.Action {
	case "accept":
		if mode == elicitationForm {
			if len(response.Content) == 0 {
				return response, errors.New("form response requires content")
			}
			shape, err := compileFormContent()
			if err != nil {
				return response, err
			}
			if err := jsonschema.Validate(shape, response.Content); err != nil {
				return response, fmt.Errorf("form response: %w", err)
			}
		} else if len(response.Content) != 0 {
			return response, errors.New("URL response cannot contain form content")
		}
	case "decline", "cancel":
		if len(response.Content) != 0 {
			return response, fmt.Errorf("%s response cannot contain form content", response.Action)
		}
	default:
		return response, errors.New("input response requires accept, decline or cancel")
	}
	return response, nil
}

// decodeElicitation rejects unsupported methods and checks the distinct form
// and URL contracts before the host can display or answer them.
func decodeElicitation(request InputRequest) (elicitationParams, error) {
	if request.Method != "elicitation/create" {
		return elicitationParams{}, fmt.Errorf("unsupported input method %q", request.Method)
	}
	var params elicitationParams
	if err := jsonv2.Unmarshal(request.Params, &params); err != nil || params.Message == nil {
		return params, errors.New("elicitation requires an object with a message")
	}
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(request.Params, &fields); err != nil {
		return params, err
	}
	// Only absence selects the default form mode. Null and an empty string are invalid.
	if mode, present := fields["mode"]; present {
		if string(mode) == "null" || json.Unmarshal(mode, &params.Mode) != nil || params.Mode == "" {
			return params, errors.New("elicitation mode must be form or url")
		}
	} else {
		params.Mode = elicitationForm
	}
	switch params.Mode {
	case elicitationForm:
		if _, present := fields["url"]; present || len(params.RequestedSchema) == 0 {
			return params, errors.New("form requires requestedSchema and cannot contain URL")
		}
		if err := jsonschema.ValidateForm(params.RequestedSchema); err != nil {
			return params, err
		}
	case "url":
		if params.URL == nil || len(params.RequestedSchema) != 0 {
			return params, errors.New("URL mode requires URL and cannot contain requestedSchema")
		}
		target, err := url.Parse(*params.URL)
		if err != nil || !target.IsAbs() {
			return params, errors.New("elicitation URL must be absolute")
		}
	default:
		return params, fmt.Errorf("unsupported elicitation mode %q", params.Mode)
	}
	return params, nil
}
