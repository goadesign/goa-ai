// A model client has one image reader. Wrappers retain that binding and all
// request preparations and observers; another binding must fail before calls.
package model

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	imageBindingProvider struct {
		*imageInputProvider
		*observerTestPreparer
	}

	imageBindingMiddleware struct {
		Provider
		TokenCounter
		*observerTestPreparer
	}
)

func TestImageSourceBindingSurvivesWrappersAndRejectsReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	var events []string
	provider := &imageInputProvider{t: t}
	baseCall := &observerTestCall{name: "base", events: &events}
	outerCall := &observerTestCall{name: "outer", events: &events}
	base, err := NewClient(&imageBindingProvider{
		imageInputProvider:   provider,
		observerTestPreparer: &observerTestPreparer{name: "base", events: &events, call: baseCall},
	})
	require.NoError(t, err)
	base, err = WithRequestPreparation(base, func(ctx context.Context, req *Request) (context.Context, *Request, error) {
		events = append(events, "before binding")
		return ctx, req, nil
	})
	require.NoError(t, err)
	image := ImagePart{Format: ImageFormatPNG, Bytes: imageSourcePNG(t)}
	reader := func(context.Context, ImageSourcePart, int64) (ImagePart, error) {
		events = append(events, "bound read")
		return image, nil
	}
	bound, err := WithImageSourceResolver(base, reader)
	require.NoError(t, err)
	prepared, err := WithRequestPreparation(bound, func(ctx context.Context, req *Request) (context.Context, *Request, error) {
		events = append(events, "after binding")
		return ctx, req, nil
	})
	require.NoError(t, err)
	wrapped, err := WrapClient(prepared, func(raw Provider) Provider {
		return &imageBindingMiddleware{
			Provider: raw, TokenCounter: raw.(TokenCounter),
			observerTestPreparer: &observerTestPreparer{name: "outer", events: &events, call: outerCall},
		}
	})
	require.NoError(t, err)
	for _, client := range []Client{bound, prepared, wrapped} {
		for _, second := range []ImageSourceResolver{reader, func(context.Context, ImageSourcePart, int64) (ImagePart, error) {
			panic("rejected replacement reader must never run")
		}} {
			replaced, err := WithImageSourceResolver(client, second)
			require.ErrorContains(t, err, "already")
			assert.Nil(t, replaced)
		}
	}
	assert.Empty(t, events)
	request := &Request{Messages: []*Message{{Role: ConversationRoleUser, Parts: []Part{
		ImageSourcePart{SourceKind: "fixture", Data: []byte(`{"id":"selected"}`)},
	}}}}
	_, err = wrapped.CountTokens(ctx, request)
	require.NoError(t, err)
	assert.Equal(t, []string{"bound read"}, events, "counts do not run preparations or call observers")
	events = nil
	_, err = wrapped.Complete(ctx, request)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(events), 5)
	assert.Equal(t, []string{"before binding", "after binding", "bound read", "prepare outer", "prepare base"}, events[:5])
	events = nil
	stream, err := wrapped.Stream(ctx, request)
	require.NoError(t, err)
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
	}
	require.NoError(t, stream.Close())
	require.GreaterOrEqual(t, len(events), 5)
	assert.Equal(t, []string{"before binding", "after binding", "bound read", "prepare outer", "prepare base"}, events[:5])
	assert.Equal(t, 2, baseCall.finishCalls)
	assert.Equal(t, 2, outerCall.finishCalls)
	assert.Zero(t, baseCall.abortCalls)
	assert.Zero(t, outerCall.abortCalls)
	require.Len(t, provider.requests, 3)
	assert.Equal(t, provider.requests[0], provider.requests[1])
	assert.Equal(t, provider.requests[0], provider.requests[2])
	// Binding returned new clients. A different run may still bind the original
	// unbound registered base without replacing this client's reader.
	_, err = WithImageSourceResolver(base, reader)
	require.NoError(t, err)
}
