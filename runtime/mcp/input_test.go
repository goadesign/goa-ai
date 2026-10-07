// These tests check the host's elicitation boundary against the current
// protocol: flat forms, URL consent, exact input IDs, and explicit capabilities.
package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestElicitationContractsAndAnswers(t *testing.T) {
	validForm := `{"message":"Select colors","requestedSchema":{"type":"object","properties":{"colors":{"type":"array","items":{"type":"string","enum":["red","blue"]}},"count":{"type":"integer","minimum":1}},"required":["colors"]}}`
	pending := &InputRequired{Requests: map[string]InputRequest{"exact-id": {Method: "elicitation/create", Params: json.RawMessage(validForm)}}}
	require.NoError(t, pending.Validate(InputSupport{Form: true}))
	require.Error(t, pending.Validate(InputSupport{URL: true}))
	for _, test := range []struct {
		name, answer string
		valid        bool
	}{
		{"accept", `{"action":"accept","content":{"colors":["red"],"count":2}}`, true},
		{"decline", `{"action":"decline"}`, true},
		{"case-distinct extensions", `{"action":"accept","ACTION":"cancel","CONTENT":false,"content":{"colors":["red"]}}`, true},
		{"missing exact action", `{"ACTION":"decline"}`, false},
		{"duplicate action", `{"action":"accept","action":"decline"}`, false},
		{"cancel", `{"action":"cancel"}`, true},
		{"invalid choice", `{"action":"accept","content":{"colors":["green"]}}`, false},
		{"nested extra value", `{"action":"accept","content":{"colors":["red"],"extra":{"hidden":true}}}`, false},
		{"null extra value", `{"action":"accept","content":{"colors":["red"],"extra":null}}`, false},
		{"missing content", `{"action":"accept"}`, false},
		{"decline data", `{"action":"decline","content":{}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := pending.ValidateResponses(map[string]json.RawMessage{"exact-id": json.RawMessage(test.answer)})
			if test.valid {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	require.Error(t, pending.ValidateResponses(map[string]json.RawMessage{"other-id": json.RawMessage(`{"action":"decline"}`)}))
	for _, invalid := range []string{
		`{"MESSAGE":"Prompt","requestedSchema":{"type":"object","properties":{}}}`,
		`{"mode":null,"message":"Prompt","requestedSchema":{"type":"object","properties":{}}}`,
		`{"mode":"","message":"Prompt","requestedSchema":{"type":"object","properties":{}}}`,
		`{"message":"Prompt","url":null,"requestedSchema":{"type":"object","properties":{}}}`,
		`{"message":"Prompt","requestedSchema":{"type":"object","properties":{"nested":{"type":"object","properties":{}}}}}`,
		`{"message":"Prompt","requestedSchema":{"type":"object","properties":{"choice":{"type":"string","enum":["a"],"enumNames":["A"]}}}}`,
	} {
		_, err := decodeElicitation(InputRequest{Method: "elicitation/create", Params: json.RawMessage(invalid)})
		require.Error(t, err, invalid)
	}
	urlInput := &InputRequired{Requests: map[string]InputRequest{"consent": {Method: "elicitation/create", Params: json.RawMessage(`{"mode":"url","message":"Authorize access","url":"https://example.test/authorize"}`)}}}
	require.NoError(t, urlInput.Validate(InputSupport{URL: true}))
	assert.NoError(t, urlInput.ValidateResponses(map[string]json.RawMessage{"consent": json.RawMessage(`{"action":"accept"}`)}))
	require.Error(t, urlInput.ValidateResponses(map[string]json.RawMessage{"consent": json.RawMessage(`{"action":"accept","content":{}}`)}))
}

// TestContinuationPresence checks that shared HTTP and stdio parameters retain
// an empty input round, and that saved non-nil answer objects keep their presence.
func TestContinuationPresence(t *testing.T) {
	for _, test := range []struct {
		name         string
		continuation *CallContinuation
		present      bool
	}{
		{name: "initial"},
		{name: "empty continuation", continuation: &CallContinuation{}, present: true},
		{name: "empty answers", continuation: &CallContinuation{InputResponses: map[string]json.RawMessage{}}, present: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			params, err := toolParams(t.Context(), CallRequest{Tool: "read", Continuation: test.continuation})
			require.NoError(t, err)
			_, present := params["inputResponses"]
			assert.Equal(t, test.present, present)
			if test.present {
				raw, err := json.Marshal(params["inputResponses"])
				require.NoError(t, err)
				assert.JSONEq(t, `{}`, string(raw))
			}
		})
	}
	raw, err := json.Marshal(CallContinuation{InputResponses: map[string]json.RawMessage{}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"inputResponses":{}}`, string(raw))
	var saved CallContinuation
	require.NoError(t, json.Unmarshal(raw, &saved))
	assert.NotNil(t, saved.InputResponses)
}

// TestMCPInputIdentifierIdentity checks arbitrary server keys through validation
// and saved continuation JSON. Answers must preserve the exact selected key;
// accepting an empty key does not allow answers for another request.
func TestMCPInputIdentifierIdentity(t *testing.T) {
	for _, id := range []string{"", " ", "profile / α"} {
		t.Run(id, func(t *testing.T) {
			pending := &InputRequired{Requests: map[string]InputRequest{id: {
				Method: "elicitation/create",
				Params: json.RawMessage(`{"mode":"form","message":"Choose","requestedSchema":{"type":"object","properties":{}}}`),
			}}}
			require.NoError(t, pending.Validate(InputSupport{Form: true}))
			answers := map[string]json.RawMessage{id: json.RawMessage(`{"action":"cancel"}`)}
			require.NoError(t, pending.ValidateResponses(answers))
			require.Error(t, pending.ValidateResponses(map[string]json.RawMessage{id + "different": json.RawMessage(`{"action":"cancel"}`)}))
			raw, err := json.Marshal(CallContinuation{InputResponses: answers})
			require.NoError(t, err)
			var saved CallContinuation
			require.NoError(t, json.Unmarshal(raw, &saved))
			assert.Equal(t, answers, saved.InputResponses)
			require.NoError(t, pending.ValidateResponses(saved.InputResponses))
		})
	}
}
