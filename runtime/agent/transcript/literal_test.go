package transcript_test

// A fragment can split a Unicode character or a JSON token. Only the completed
// canonical value is decoded; no prefix of a malformed value becomes history.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestLiteralDecoderAcrossEveryByte(t *testing.T) {
	data := []byte(`[{"role":"user","parts":[{"kind":"text","text":"雪 🌱"}]}]`)
	want, err := transcript.DecodeRunLogDelta(data)
	require.NoError(t, err)
	var decoder transcript.LiteralDecoder
	for index := range data {
		messages, err := decoder.Append(t.Context(), storage.LiteralPart{Data: data[index : index+1], Final: index == len(data)-1})
		require.NoError(t, err)
		if index != len(data)-1 {
			require.Nil(t, messages)
			require.Error(t, decoder.Finish())
		} else {
			require.Equal(t, want, messages)
		}
	}
	require.NoError(t, decoder.Finish())
	messages, err := decoder.Append(t.Context(), storage.LiteralPart{Data: data, Final: true})
	require.NoError(t, err)
	require.Equal(t, want, messages, "the next literal starts a new byte sequence")
}

func TestLiteralDecoderRejectsCompletedInvalidContent(t *testing.T) {
	for _, input := range []string{
		`[]`, `null`, `[null]`, `[`, `{}`, `[] []`,
		`[{"role":"user","parts":[{"kind":"unknown"}]}]`,
		"[{\"role\":\"user\",\"parts\":[{\"kind\":\"text\",\"text\":\"\xff\"}]}]",
	} {
		t.Run(input, func(t *testing.T) {
			var decoder transcript.LiteralDecoder
			messages, err := decoder.Append(t.Context(), storage.LiteralPart{Data: []byte(input), Final: true})
			require.Error(t, err)
			require.Nil(t, messages)
		})
	}
	var decoder transcript.LiteralDecoder
	messages, err := decoder.Append(t.Context(), storage.LiteralPart{Final: true})
	require.Error(t, err)
	require.Nil(t, messages)
	ctx, cancel := context.WithCancel(t.Context())
	require.NoError(t, decoder.Finish())
	messages, err = decoder.Append(t.Context(), storage.LiteralPart{Data: []byte(strings.Repeat(" ", 1024))})
	require.NoError(t, err)
	require.Nil(t, messages)
	cancel()
	messages, err = decoder.Append(ctx, storage.LiteralPart{Data: []byte("[]"), Final: true})
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, messages)
}
