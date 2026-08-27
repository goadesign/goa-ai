// This file verifies that Bedrock uses its normalized tool name only for
// lookup while retaining the exact prefixed name returned by the service.

package bedrock

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type lateErrorBedrockReader struct {
	events <-chan brtypes.ConverseStreamOutput
	err    error
}

func (r *lateErrorBedrockReader) Events() <-chan brtypes.ConverseStreamOutput {
	return r.events
}

func (*lateErrorBedrockReader) Close() error {
	return nil
}

func (r *lateErrorBedrockReader) Err() error {
	return r.err
}

func TestTranslateResponsePreservesPrefixedUnadvertisedToolName(t *testing.T) {
	output := &bedrockruntime.ConverseOutput{
		StopReason: brtypes.StopReasonToolUse,
		Output: &brtypes.ConverseOutputMemberMessage{Value: brtypes.Message{
			Role: brtypes.ConversationRoleAssistant,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberText{Value: "partial text"},
				&brtypes.ContentBlockMemberToolUse{Value: brtypes.ToolUseBlock{
					ToolUseId: strPtr("call-1"),
					Name:      strPtr("$FUNCTIONS.catalog_list_items"),
					Input:     smithyDocumentFromJSON(t, `{}`),
				}},
				&brtypes.ContentBlockMemberToolUse{Value: brtypes.ToolUseBlock{
					ToolUseId: strPtr("call-2"),
					Name:      strPtr("$FUNCTIONS.catalog_list_nearby"),
					Input:     smithyDocumentFromJSON(t, `{"ignored":true}`),
				}},
			},
		}},
	}

	response, err := translateResponse(
		output,
		map[string]string{"catalog_list_items": "catalog.list_items"},
		"test-model",
		model.ModelClassDefault,
	)

	assert.Nil(t, response)
	name, ok := model.UnadvertisedToolName(err)
	assert.True(t, ok)
	assert.Equal(t, "$FUNCTIONS.catalog_list_nearby", name)
}

func TestTranslateResponseAcceptsPrefixedAdvertisedToolName(t *testing.T) {
	output := &bedrockruntime.ConverseOutput{
		StopReason: brtypes.StopReasonToolUse,
		Output: &brtypes.ConverseOutputMemberMessage{Value: brtypes.Message{
			Role: brtypes.ConversationRoleAssistant,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberToolUse{Value: brtypes.ToolUseBlock{
					ToolUseId: strPtr("call-1"),
					Name:      strPtr("$FUNCTIONS.catalog_list_items"),
					Input:     smithyDocumentFromJSON(t, `{}`),
				}},
			},
		}},
	}

	response, err := translateResponse(
		output,
		map[string]string{"catalog_list_items": "catalog.list_items"},
		"test-model",
		model.ModelClassDefault,
	)

	require.NoError(t, err)
	require.Len(t, response.ToolCalls(), 1)
	assert.Equal(t, "catalog.list_items", string(response.ToolCalls()[0].Name))
}

func TestStreamPreservesPrefixedUnadvertisedToolName(t *testing.T) {
	request := &model.Request{
		Model:      "test-model",
		ModelClass: model.ModelClassDefault,
		Tools: []*model.ToolDefinition{{
			Name:  "catalog.list_items",
			Input: mustBedrockToolInput(t, rawjson.Message(`{"type":"object"}`)),
		}},
	}
	contract, err := model.NewRequestContract(request)
	require.NoError(t, err)
	textIndex := int32(0)
	validIndex := int32(1)
	invalidIndex := int32(2)
	inputTokens := int32(4)
	outputTokens := int32(3)
	totalTokens := int32(7)
	events := make(chan brtypes.ConverseStreamOutput, 9)
	events <- &brtypes.ConverseStreamOutputMemberMessageStart{}
	events <- &brtypes.ConverseStreamOutputMemberContentBlockDelta{
		Value: brtypes.ContentBlockDeltaEvent{
			ContentBlockIndex: &textIndex,
			Delta:             &brtypes.ContentBlockDeltaMemberText{Value: "partial text"},
		},
	}
	events <- &brtypes.ConverseStreamOutputMemberContentBlockStop{
		Value: brtypes.ContentBlockStopEvent{ContentBlockIndex: &textIndex},
	}
	events <- &brtypes.ConverseStreamOutputMemberContentBlockStart{
		Value: brtypes.ContentBlockStartEvent{
			ContentBlockIndex: &validIndex,
			Start: &brtypes.ContentBlockStartMemberToolUse{Value: brtypes.ToolUseBlockStart{
				ToolUseId: strPtr("call-1"),
				Name:      strPtr("$FUNCTIONS.catalog_list_items"),
			}},
		},
	}
	events <- &brtypes.ConverseStreamOutputMemberContentBlockStop{
		Value: brtypes.ContentBlockStopEvent{ContentBlockIndex: &validIndex},
	}
	events <- &brtypes.ConverseStreamOutputMemberContentBlockStart{
		Value: brtypes.ContentBlockStartEvent{
			ContentBlockIndex: &invalidIndex,
			Start: &brtypes.ContentBlockStartMemberToolUse{Value: brtypes.ToolUseBlockStart{
				ToolUseId: strPtr("call-2"),
				Name:      strPtr("$FUNCTIONS.catalog_list_nearby"),
			}},
		},
	}
	events <- &brtypes.ConverseStreamOutputMemberContentBlockStop{
		Value: brtypes.ContentBlockStopEvent{ContentBlockIndex: &invalidIndex},
	}
	events <- &brtypes.ConverseStreamOutputMemberMessageStop{
		Value: brtypes.MessageStopEvent{StopReason: brtypes.StopReasonToolUse},
	}
	events <- &brtypes.ConverseStreamOutputMemberMetadata{
		Value: brtypes.ConverseStreamMetadataEvent{Usage: &brtypes.TokenUsage{
			InputTokens:  &inputTokens,
			OutputTokens: &outputTokens,
			TotalTokens:  &totalTokens,
		}},
	}
	close(events)
	reader := &nonIdempotentBedrockReader{events: events}
	providerStream := bedrockruntime.NewConverseStreamEventStream(
		func(stream *bedrockruntime.ConverseStreamEventStream) {
			stream.Reader = reader
		},
	)
	raw := newBedrockStreamer(
		context.Background(),
		providerStream,
		map[string]string{"catalog_list_items": "catalog.list_items"},
		"test-model",
		model.ModelClassDefault,
		nil,
		"",
		nil,
		map[string]struct{}{"catalog.list_items": {}},
		contract,
	)
	streamer, err := contract.ValidateStream(raw)
	require.NoError(t, err)
	defer func() {
		assert.NoError(t, streamer.Close())
	}()
	var chunks []model.Chunk
	for {
		chunk, recvErr := streamer.Recv()
		if recvErr != nil {
			err = recvErr
			break
		}
		chunks = append(chunks, chunk)
	}

	require.NotErrorIs(t, err, io.EOF)
	var validationErr *model.OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	name, ok := model.UnadvertisedToolName(validationErr)
	assert.True(t, ok)
	assert.Equal(t, "$FUNCTIONS.catalog_list_nearby", name)
	assert.Equal(t, &model.TokenUsage{
		Model:        "test-model",
		ModelClass:   model.ModelClassDefault,
		InputTokens:  4,
		OutputTokens: 3,
		TotalTokens:  7,
	}, validationErr.Usage())
	assert.Nil(t, streamer.Response())
	require.Len(t, chunks, 1)
	assert.Equal(t, "partial text", chunks[0].(model.TextChunk).Message.Parts[0].(model.TextPart).Text)
}

func TestStreamTransportFailureSupersedesLatchedUnadvertisedName(t *testing.T) {
	index := int32(0)
	transportErr := errors.New("connection lost")
	events := make(chan brtypes.ConverseStreamOutput, 2)
	events <- &brtypes.ConverseStreamOutputMemberMessageStart{}
	events <- &brtypes.ConverseStreamOutputMemberContentBlockStart{
		Value: brtypes.ContentBlockStartEvent{
			ContentBlockIndex: &index,
			Start: &brtypes.ContentBlockStartMemberToolUse{Value: brtypes.ToolUseBlockStart{
				ToolUseId: strPtr("call-1"),
				Name:      strPtr("$FUNCTIONS.missing"),
			}},
		},
	}
	close(events)
	providerStream := bedrockruntime.NewConverseStreamEventStream(
		func(stream *bedrockruntime.ConverseStreamEventStream) {
			stream.Reader = &lateErrorBedrockReader{
				events: events,
				err:    transportErr,
			}
		},
	)
	contract, err := model.NewRequestContract(&model.Request{})
	require.NoError(t, err)
	streamer := newBedrockStreamer(
		context.Background(),
		providerStream,
		nil,
		"test-model",
		model.ModelClassDefault,
		nil,
		"",
		nil,
		nil,
		contract,
	)

	_, err = streamer.Recv()

	_, recoverable := model.UnadvertisedToolName(err)
	assert.False(t, recoverable)
	_, providerFailure := model.AsProviderError(err)
	assert.True(t, providerFailure)
	assert.ErrorIs(t, streamer.Close(), transportErr)
}

func TestStreamCancellationSupersedesLatchedUnadvertisedName(t *testing.T) {
	index := int32(0)
	events := make(chan brtypes.ConverseStreamOutput)
	latched := make(chan struct{})
	release := make(chan struct{})
	go func() {
		events <- &brtypes.ConverseStreamOutputMemberMessageStart{}
		events <- &brtypes.ConverseStreamOutputMemberContentBlockStart{
			Value: brtypes.ContentBlockStartEvent{
				ContentBlockIndex: &index,
				Start: &brtypes.ContentBlockStartMemberToolUse{Value: brtypes.ToolUseBlockStart{
					ToolUseId: strPtr("call-1"),
					Name:      strPtr("$FUNCTIONS.missing"),
				}},
			},
		}
		events <- &brtypes.ConverseStreamOutputMemberContentBlockStop{
			Value: brtypes.ContentBlockStopEvent{ContentBlockIndex: &index},
		}
		close(latched)
		<-release
		close(events)
	}()
	reader := &nonIdempotentBedrockReader{events: events}
	providerStream := bedrockruntime.NewConverseStreamEventStream(
		func(stream *bedrockruntime.ConverseStreamEventStream) {
			stream.Reader = reader
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	contract, err := model.NewRequestContract(&model.Request{})
	require.NoError(t, err)
	streamer := newBedrockStreamer(
		ctx,
		providerStream,
		nil,
		"test-model",
		model.ModelClassDefault,
		nil,
		"",
		nil,
		nil,
		contract,
	)
	<-latched
	cancel()
	for err == nil {
		_, err = streamer.Recv()
	}
	close(release)

	require.ErrorIs(t, err, context.Canceled)
	_, recoverable := model.UnadvertisedToolName(err)
	assert.False(t, recoverable)
	assert.NoError(t, streamer.Close())
}
