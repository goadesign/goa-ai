// These tests retain generated Registry field paths and branch selections
// through workflow encoding while preserving the shared validation and budget.
package workflowcodec

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

func TestDataConverterRetainsRegistryOperationUnions(t *testing.T) {
	field := genregistry.NewToolFieldSegmentField("records")
	element := genregistry.NewToolFieldSegmentElement(&genregistry.ToolCollectionElement{})
	tagged := genregistry.NewToolUnionSelectionTagged(&genregistry.ToolTaggedUnionBranch{
		Discriminator: []*genregistry.ToolFieldPathSegment{{Segment: field}}, Value: "record",
	})
	untagged := genregistry.NewToolUnionSelectionUntagged(&genregistry.ToolUntaggedUnionBranch{
		Path: []*genregistry.ToolFieldPathSegment{{Segment: element}}, JSONKind: "object", Index: 1,
	})
	input := &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}, Operation: &genregistry.ToolSchema{ConsumerContract: &genregistry.ConsumerContract{
		Payload: &genregistry.ToolTypeMetadata{Fields: []*genregistry.ToolFieldMetadata{{
			Path:     []*genregistry.ToolFieldPathSegment{{Segment: field}, {Segment: element}},
			Branches: []*genregistry.ToolUnionBranch{{Selection: tagged}, {Selection: untagged}},
		}}},
	}}}
	converter := NewDataConverter()
	payload, err := converter.ToPayload(input)
	require.NoError(t, err)
	var saved api.RunInput
	require.NoError(t, converter.FromPayload(payload, &saved))
	assert.Equal(t, input, &saved)
	var budget Budget
	require.NoError(t, budget.AddEncodedSource(input))
	assert.GreaterOrEqual(t, budget.used, len(payload.Data))

	for _, union := range []any{field, &field, element, &element, tagged, &tagged, untagged, &untagged} {
		_, err := converter.ToPayload(union)
		assert.NoError(t, err)
	}
}

func TestDataConverterRejectsInvalidRegistryOperationUnions(t *testing.T) {
	for _, union := range []any{
		genregistry.ToolFieldSegment{}, genregistry.ToolUnionSelection{},
		genregistry.NewToolFieldSegmentElement(nil), genregistry.NewToolUnionSelectionTagged(nil),
	} {
		_, err := NewDataConverter().ToPayload(union)
		require.Error(t, err)
		var budget Budget
		require.Error(t, budget.AddEncodedSource(union))
	}
	union := genregistry.NewToolUnionSelectionTagged(&genregistry.ToolTaggedUnionBranch{
		Value: strings.Repeat("x", engine.MaxPayloadBytes+1),
	})
	_, err := NewDataConverter().ToPayload(union)
	require.ErrorContains(t, err, "maximum aggregate size")
	var budget Budget
	assert.ErrorContains(t, budget.AddEncodedSource(union), "exceeds conservative encoded-size bound")
}
