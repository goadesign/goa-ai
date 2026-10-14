// Package mcp parses public host questions using the same rules as execution. The private
// request retains its original bytes; this value contains the validated form
// or consent request that a caller needs to present and answer.
package mcp

import (
	"slices"

	genhostinput "goa.design/goa-ai/registry/gen/hostinput"
)

// PublicInput returns the validated host question without private service state.
// An omitted mode selects form through the existing MCP parser; the original
// parameter bytes and protocol response validation remain unchanged.
func (r InputRequest) PublicInput() (*genhostinput.HostInputRequest, error) {
	params, err := decodeElicitation(r)
	if err != nil {
		return nil, err
	}
	result := &genhostinput.HostInputRequest{Extensions: make(map[string][]byte, len(params.Extensions))}
	for name, value := range params.Extensions {
		result.Extensions[name] = slices.Clone(value)
	}
	if params.Mode == elicitationForm {
		result.Question = genhostinput.NewHostQuestionForm(&genhostinput.HostFormParams{Message: *params.Message, RequestedSchema: slices.Clone(params.RequestedSchema)})
	} else {
		result.Question = genhostinput.NewHostQuestionURL(&genhostinput.HostURLParams{Message: *params.Message, URL: *params.URL})
	}
	return result, nil
}
