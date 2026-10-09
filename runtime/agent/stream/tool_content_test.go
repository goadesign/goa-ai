// These tests deliver typed tool content to the host in invocation order without
// sharing mutable values with the event retained by the runtime.
package stream

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/content"
)

func TestToolEndRetainsContentOnSuccessAndFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		blocks := content.Blocks{
			&content.TextContent{Text: "complete", Meta: []byte(`{"sequence":9007199254740993}`)},
			&content.ImageContent{Data: "AQID", MIMEType: "image/png"},
			&content.AudioContent{Data: "BAUG", MIMEType: "audio/wav"},
			&content.ResourceLink{Name: "report", URI: "https://example.com/report"},
			&content.EmbeddedResource{Resource: &content.TextResourceContents{URI: "report://inline", Text: "accepted"}},
		}
		var failure *planner.ToolFailure
		if failed {
			failure = &planner.ToolFailure{Kind: planner.FailureDomainRejection, Error: planner.NewToolError("rejected"), Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan}}
		}
		sink := &mockSink{}
		subscriber, err := NewSubscriber(sink, RuntimeHostProfile())
		require.NoError(t, err)
		event := hooks.NewToolResultReceivedEvent("result-run", "service.agent", "session-1", "call-run", "remote.lookup", "call-1", "", nil, nil, blocks, "preview", nil, 0, nil, failure)
		require.NoError(t, subscriber.HandleEvent(t.Context(), event))
		require.Len(t, sink.events, 1)
		end := sink.events[0].(ToolEnd)
		assert.Equal(t, blocks, end.Data.Blocks)
		assert.Equal(t, "call-run", end.Data.CallRunID)
		assert.Equal(t, "call-1", end.Data.ToolCallID)
		assert.Equal(t, failed, end.Data.Failure != nil)
		end.Data.Blocks[0].(*content.TextContent).Text = "changed"
		assert.Equal(t, "complete", event.Blocks[0].(*content.TextContent).Text)
		assert.Equal(t, "complete", blocks[0].(*content.TextContent).Text)
	}
}
