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

func newMalformedBedrockStreamer(t *testing.T, source []brtypes.ConverseStreamOutput) model.Streamer {
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
		nil,
		"test-model",
		model.ModelClassDefault,
		nil,
		"",
		nil,
		nil,
		contract,
	)
}
