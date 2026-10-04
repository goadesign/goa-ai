// These tests exercise the public successful-result boundary with the existing
// runtime codec fixtures. Semantic JSON, selected specs, and bounds remain owned
// by the caller on both acceptance and rejection.
package runtime

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestValidateSuccessfulToolResultSemanticContract(t *testing.T) {
	t.Parallel()

	unbounded := newProjectedResultSpec()
	unbounded.Bounds = nil
	noResult := tools.ToolSpec{Name: "tool"}
	typedNil := unbounded
	// The existing planner-final-result test supplies the same codec outcome:
	// a nil pointer returned as an any value must not become a successful result.
	typedNil.Result.Codec.FromJSON = func([]byte) (any, error) {
		var result *projectedRuntimeResult
		return result, nil
	}
	for _, test := range []struct {
		name   string
		spec   tools.ToolSpec
		result rawjson.Message
		want   string
	}{
		{name: "no result", spec: noResult},
		{name: "no result with null", spec: noResult, result: rawjson.Message(`null`), want: "does not define a result but contains one"},
		{name: "no result with whitespace", spec: noResult, result: rawjson.Message(` `), want: "does not define a result but contains one"},
		{name: "no result with object", spec: noResult, result: rawjson.Message(`{"status":"ok"}`), want: "does not define a result but contains one"},
		{name: "declared result missing", spec: unbounded, want: "tool result is missing"},
		{name: "malformed result", spec: unbounded, result: rawjson.Message(`{`), want: "does not satisfy its generated contract"},
		{name: "wrong result field type", spec: unbounded, result: rawjson.Message(`{"results":1}`), want: "does not satisfy its generated contract"},
		{name: "nil decoded result", spec: unbounded, result: rawjson.Message(`null`), want: "tool result decoded to nil"},
		{name: "typed nil decoded result", spec: typedNil, result: rawjson.Message(`{}`), want: "tool result decoded to nil"},
		{name: "typed result", spec: unbounded, result: rawjson.Message(`{"results":["alpha"]}`)},
		{name: "dynamic result", spec: newAnyJSONSpec("tool"), result: rawjson.Message(`{"status":"ok","records":[{"name":"alpha"}]}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := rawjson.Message(bytes.Clone(test.result))

			err := ValidateSuccessfulToolResult(test.spec, test.result, nil)

			if test.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, test.want)
			}
			assert.Equal(t, before, test.result)
		})
	}
}

func TestValidateSuccessfulToolResultBoundsContract(t *testing.T) {
	t.Parallel()

	bounded := newProjectedResultSpec()
	unbounded := newProjectedResultSpec()
	unbounded.Bounds = nil
	withoutPaging := newProjectedResultSpec()
	withoutPaging.Bounds = &tools.BoundsSpec{}
	cursor, empty := projectedResultNextCursor, ""
	for _, test := range []struct {
		name   string
		spec   tools.ToolSpec
		bounds *agent.Bounds
		want   string
	}{
		{name: "unbounded success", spec: unbounded},
		{name: "unbounded with bounds", spec: unbounded, bounds: &agent.Bounds{Returned: 1}, want: "unbounded tool"},
		{name: "bounded missing bounds", spec: bounded, want: "result without bounds"},
		{name: "bounded complete", spec: bounded, bounds: &agent.Bounds{Returned: 1}},
		{name: "truncated without continuation", spec: bounded, bounds: &agent.Bounds{Returned: 1, Truncated: true}, want: "truncated result without next_cursor or refinement_hint"},
		{name: "empty cursor even with refinement", spec: bounded, bounds: &agent.Bounds{Truncated: true, NextCursor: &empty, RefinementHint: "narrow by source"}, want: "empty next_cursor"},
		{name: "cursor without paging", spec: withoutPaging, bounds: &agent.Bounds{Truncated: true, NextCursor: &cursor}, want: "paging is not configured"},
		{name: "cursor without truncation", spec: bounded, bounds: &agent.Bounds{NextCursor: &cursor}, want: "next_cursor without truncation"},
		{name: "paged continuation", spec: bounded, bounds: &agent.Bounds{Returned: 1, Truncated: true, NextCursor: &cursor}},
		{name: "refinement without paging", spec: withoutPaging, bounds: &agent.Bounds{Returned: 1, Truncated: true, RefinementHint: "narrow by source"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := rawjson.Message(`{"results":["alpha"]}`)
			beforeResult := rawjson.Message(bytes.Clone(result))
			beforeBounds := agent.CloneBounds(test.bounds)

			err := ValidateSuccessfulToolResult(test.spec, result, test.bounds)

			if test.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, test.want)
			}
			assert.Equal(t, beforeResult, result)
			assert.Equal(t, beforeBounds, test.bounds)
		})
	}
}

func TestValidateSuccessfulToolResultPreservesInputs(t *testing.T) {
	t.Parallel()

	for _, invalid := range []bool{false, true} {
		name := "accepted"
		if invalid {
			name = "rejected"
		}
		t.Run(name, func(t *testing.T) {
			spec := newProjectedResultSpec()
			result := rawjson.Message(" \n{ \"results\": [\"alpha\"] }\t")
			total, cursor := 9, projectedResultNextCursor
			bounds := &agent.Bounds{
				Returned:       1,
				Total:          &total,
				Truncated:      true,
				NextCursor:     &cursor,
				RefinementHint: "narrow by source",
			}
			if invalid {
				bounds.Truncated = false
			}
			beforeResult := rawjson.Message(bytes.Clone(result))
			beforeBounds := agent.CloneBounds(bounds)
			beforePaging := *spec.Bounds.Paging
			beforePayloadSchema := rawjson.Message(bytes.Clone(spec.Payload.Schema))
			beforeName, beforeResultName := spec.Name, spec.Result.Name
			boundsSpec, pagingSpec := spec.Bounds, spec.Bounds.Paging

			err := ValidateSuccessfulToolResult(spec, result, bounds)

			if invalid {
				require.ErrorContains(t, err, "next_cursor without truncation")
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, beforeResult, result)
			assert.Equal(t, beforeBounds, bounds)
			assert.Same(t, &total, bounds.Total)
			assert.Same(t, &cursor, bounds.NextCursor)
			assert.Same(t, boundsSpec, spec.Bounds)
			assert.Same(t, pagingSpec, spec.Bounds.Paging)
			assert.Equal(t, beforePaging, *spec.Bounds.Paging)
			assert.Equal(t, beforePayloadSchema, spec.Payload.Schema)
			assert.Equal(t, beforeName, spec.Name)
			assert.Equal(t, beforeResultName, spec.Result.Name)
		})
	}
}

func TestValidateSuccessfulToolResultPreservesCodecCauseAndOrder(t *testing.T) {
	t.Parallel()

	// This bounded spec also requires bounds, but malformed semantic output
	// remains the first error, with the actual fixture codec's cause preserved.
	err := ValidateSuccessfulToolResult(newProjectedResultSpec(), rawjson.Message(`{"results":1}`), nil)

	require.ErrorContains(t, err, "does not satisfy its generated contract")
	var cause *json.UnmarshalTypeError
	require.ErrorAs(t, err, &cause)
	assert.Equal(t, "results", cause.Field)
}

func TestValidateSuccessfulToolResultKeepsPrivateCallDiagnostics(t *testing.T) {
	t.Parallel()

	spec := newProjectedResultSpec()
	result := rawjson.Message(`{"results":["alpha"]}`)
	call := ToolCall{Name: spec.Name, ToolCallID: "tool-1"}

	_, err := validatePersistedToolResult(&spec, call, result, nil, nil, nil, nil)
	require.ErrorContains(t, err, "result without bounds")
	require.ErrorContains(t, err, "tool_call_id=tool-1")

	err = ValidateSuccessfulToolResult(spec, result, nil)
	require.ErrorContains(t, err, `bounded tool "tool"`)
	require.ErrorContains(t, err, "result without bounds")
	assert.NotContains(t, err.Error(), "tool_call_id=tool-1")
}
