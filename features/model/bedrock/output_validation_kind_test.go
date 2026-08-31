// Bedrock output-classification tests feed malformed SDK events through the
// asynchronous stream pump and verify that event shape and ordering failures
// retain their distinct public categories.
package bedrock

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestStreamPumpClassifiesMalformedEvents(t *testing.T) {
	tests := []struct {
		name    string
		events  []brtypes.ConverseStreamOutput
		kind    model.OutputValidationKind
		message string
	}{
		{
			name: "duplicate message start",
			events: []brtypes.ConverseStreamOutput{
				&brtypes.ConverseStreamOutputMemberMessageStart{},
				&brtypes.ConverseStreamOutputMemberMessageStart{},
			},
			kind:    model.OutputValidationStreamProtocol,
			message: "duplicate message start",
		},
		{
			name: "missing content block index",
			events: []brtypes.ConverseStreamOutput{
				&brtypes.ConverseStreamOutputMemberMessageStart{},
				&brtypes.ConverseStreamOutputMemberContentBlockStop{},
			},
			kind:    model.OutputValidationResponseShape,
			message: "content block index missing",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			streamer := newMalformedBedrockStreamer(t, test.events)

			_, err := streamer.Recv()

			var validationErr *model.OutputValidationError
			require.ErrorAs(t, err, &validationErr)
			require.Equal(t, test.kind, validationErr.Kind())
			require.ErrorContains(t, errors.Unwrap(validationErr), test.message)
			require.NoError(t, streamer.Close())
		})
	}
}

func TestStreamPumpDefersMalformedToolJSONUntilUsage(t *testing.T) {
	index := int32(0)
	providerName := "tasks_progress_complete"
	malformed := `{"title":"Weekly review"`
	inputTokens := int32(85_510)
	outputTokens := int32(3_745)
	totalTokens := int32(89_255)
	events := []brtypes.ConverseStreamOutput{
		&brtypes.ConverseStreamOutputMemberMessageStart{},
		&brtypes.ConverseStreamOutputMemberContentBlockStart{
			Value: brtypes.ContentBlockStartEvent{
				ContentBlockIndex: &index,
				Start: &brtypes.ContentBlockStartMemberToolUse{
					Value: brtypes.ToolUseBlockStart{
						Name:      &providerName,
						ToolUseId: strPtr("tooluse_1"),
					},
				},
			},
		},
		&brtypes.ConverseStreamOutputMemberContentBlockDelta{
			Value: brtypes.ContentBlockDeltaEvent{
				ContentBlockIndex: &index,
				Delta: &brtypes.ContentBlockDeltaMemberToolUse{
					Value: brtypes.ToolUseBlockDelta{Input: &malformed},
				},
			},
		},
		&brtypes.ConverseStreamOutputMemberContentBlockStop{
			Value: brtypes.ContentBlockStopEvent{ContentBlockIndex: &index},
		},
		&brtypes.ConverseStreamOutputMemberMessageStop{
			Value: brtypes.MessageStopEvent{StopReason: brtypes.StopReasonToolUse},
		},
		&brtypes.ConverseStreamOutputMemberMetadata{
			Value: brtypes.ConverseStreamMetadataEvent{
				Usage: &brtypes.TokenUsage{
					InputTokens:  &inputTokens,
					OutputTokens: &outputTokens,
					TotalTokens:  &totalTokens,
				},
			},
		},
	}
	streamer := newMalformedBedrockStreamerWithNames(
		t,
		events,
		map[string]string{providerName: "tasks.progress.complete"},
	)

	chunk, err := streamer.Recv()
	require.NoError(t, err)
	require.IsType(t, model.ToolCallDeltaChunk{}, chunk)
	_, err = streamer.Recv()

	var validationErr *model.OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, model.OutputValidationToolArguments, validationErr.Kind())
	require.NotEmpty(t, validationErr.RecoveryCorrection())
	require.NotContains(t, validationErr.RecoveryCorrection(), "Weekly review")
	require.Equal(t, int(totalTokens), validationErr.Usage().TotalTokens)
	require.NoError(t, streamer.Close())
}

func newMalformedBedrockStreamer(t *testing.T, source []brtypes.ConverseStreamOutput) model.Streamer {
	return newMalformedBedrockStreamerWithNames(t, source, nil)
}

// newMalformedBedrockStreamerWithNames builds the real asynchronous adapter
// with the provider-to-canonical tool names used by the supplied events.
func newMalformedBedrockStreamerWithNames(
	t *testing.T,
	source []brtypes.ConverseStreamOutput,
	nameMap map[string]string,
) model.Streamer {
	t.Helper()
	events := make(chan brtypes.ConverseStreamOutput, len(source))
	for _, event := range source {
		events <- event
	}
	close(events)
	reader := &nonIdempotentBedrockReader{events: events}
	providerStream := bedrockruntime.NewConverseStreamEventStream(
		func(stream *bedrockruntime.ConverseStreamEventStream) {
			stream.Reader = reader
		},
	)
	contract, err := model.NewRequestContract(&model.Request{})
	require.NoError(t, err)
	return newBedrockStreamer(
		context.Background(),
		providerStream,
		nameMap,
		"test-model",
		model.ModelClassDefault,
		nil,
		"",
		nil,
		nil,
		contract,
	)
}
