package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type imageInputProvider struct {
	t        *testing.T
	requests [][]byte
}

func (p *imageInputProvider) capture(request *Request) {
	p.t.Helper()
	_, err := NewRequestContract(request)
	require.NoError(p.t, err)
	data, err := json.Marshal(request)
	require.NoError(p.t, err)
	p.requests = append(p.requests, data)
	for _, message := range request.Messages {
		for _, part := range message.Parts {
			if image, ok := part.(ImagePart); ok {
				image.Bytes[0] = 0
			}
		}
	}
}

func (p *imageInputProvider) CountTokens(ctx context.Context, request *Request) (TokenCount, error) {
	p.capture(request)
	return TokenCount{Model: "fixture", ModelClass: request.ModelClass, InputTokens: 10, Exact: false}, ctx.Err()
}

func (p *imageInputProvider) Complete(_ context.Context, request *Request) (*Response, error) {
	p.capture(request)
	return &Response{Content: []Message{{Role: ConversationRoleAssistant, Parts: []Part{TextPart{Text: "seen"}}}}, StopReason: "stop"}, nil
}

func (p *imageInputProvider) Stream(_ context.Context, request *Request) (Streamer, error) {
	p.capture(request)
	return &validatedStreamFixture{
		chunks: []Chunk{
			TextChunk{Message: Message{Role: ConversationRoleAssistant, Parts: []Part{TextPart{Text: "seen"}}}},
			StopChunk{Reason: "stop"},
		},
		response: &Response{Content: []Message{{Role: ConversationRoleAssistant, Parts: []Part{TextPart{Text: "seen"}}}}, StopReason: "stop"},
	}, nil
}

func TestImageSourceCountCompleteStreamUseSameOwnedInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	provider := &imageInputProvider{t: t}
	client, err := NewClient(provider)
	require.NoError(t, err)
	body := imageSourcePNG(t)
	var reads int
	client, err = WithImageSourceResolver(client, func(_ context.Context, source ImageSourcePart, remaining int64) (ImagePart, error) {
		reads++
		assert.Equal(t, `{"id":"selected"}`, string(source.Data)) //nolint:testifylint // The reader receives the saved bytes without rewriting.
		assert.Greater(t, remaining, int64(len(body)))
		source.Data[0] = '['
		return ImagePart{Format: ImageFormatPNG, Bytes: body}, nil
	})
	require.NoError(t, err)
	request := &Request{
		Model: "fixture", MaxTokens: 41, Temperature: 0.2,
		Thinking: &ThinkingOptions{Enable: true, BudgetTokens: 9},
		Cache:    &CacheOptions{AfterSystem: true},
		Messages: []*Message{{Role: ConversationRoleUser, Parts: []Part{
			TextPart{Text: "Inspect this exact image."},
			ImageSourcePart{SourceKind: "fixture.image.v1", Data: []byte(`{"id":"selected"}`)},
		}}},
	}
	before, err := json.Marshal(request)
	require.NoError(t, err)
	count, err := client.CountTokens(ctx, request)
	require.NoError(t, err)
	assert.False(t, count.Exact)
	_, err = client.Complete(ctx, request)
	require.NoError(t, err)
	stream, err := client.Stream(ctx, request)
	require.NoError(t, err)
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
	}
	require.NoError(t, stream.Close())
	require.Len(t, provider.requests, 3)
	assert.Equal(t, provider.requests[0], provider.requests[1])
	assert.Equal(t, provider.requests[0], provider.requests[2])
	assert.Equal(t, 3, reads)
	assert.Equal(t, byte(137), body[0])
	after, err := json.Marshal(request)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestImageSourceLoadsOnlyAfterOrdinaryPreparation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	provider := &imageInputProvider{t: t}
	client, err := NewClient(provider)
	require.NoError(t, err)
	var read []string
	body := imageSourcePNG(t)
	client, err = WithImageSourceResolver(client, func(_ context.Context, source ImageSourcePart, _ int64) (ImagePart, error) {
		read = append(read, source.SourceKind)
		return ImagePart{Format: ImageFormatPNG, Bytes: body}, nil
	})
	require.NoError(t, err)
	client, err = WithRequestPreparation(client, func(ctx context.Context, request *Request) (context.Context, *Request, error) {
		require.Empty(t, read)
		request.Messages = request.Messages[len(request.Messages)-1:]
		return ctx, request, nil
	})
	require.NoError(t, err)
	request := &Request{}
	for range 20 {
		request.Messages = append(request.Messages, &Message{Role: ConversationRoleUser, Parts: []Part{
			ImageSourcePart{SourceKind: "unused", Data: []byte(`{}`)},
		}})
	}
	request.Messages = append(request.Messages, &Message{Role: ConversationRoleUser, Parts: []Part{
		ImageSourcePart{SourceKind: "selected", Data: []byte(`{}`)},
	}})
	_, err = client.Complete(ctx, request)
	require.NoError(t, err)
	assert.Equal(t, []string{"selected"}, read)
	assert.Len(t, request.Messages, 21)
}

func TestImageSourceRejectsBeforeReadOrDispatch(t *testing.T) {
	for _, name := range []string{"deadline", "canceled", "bytes", "visits", "schema", "role", "malformed", "counter unsupported"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			provider := &imageInputProvider{t: t}
			var raw Provider = provider
			if name == "counter unsupported" {
				raw = &clientTestProvider{}
			}
			client, err := NewClient(raw)
			require.NoError(t, err)
			var reads int
			client, err = WithImageSourceResolver(client, func(context.Context, ImageSourcePart, int64) (ImagePart, error) {
				reads++
				return ImagePart{}, errors.New("unexpected read")
			})
			require.NoError(t, err)
			request := &Request{Messages: []*Message{{Role: ConversationRoleUser, Parts: []Part{
				ImageSourcePart{SourceKind: "fixture", Data: []byte(`{}`)},
			}}}}
			switch name {
			case "deadline":
				ctx = context.Background()
			case "canceled":
				cancel()
			case "bytes":
				request.Messages[0].Parts = append(request.Messages[0].Parts, TextPart{Text: strings.Repeat("x", maxDynamicValueBytes)})
			case "visits":
				request.Tools = make([]*ToolDefinition, maxDynamicValueVisits+1)
			case "schema":
				request.Tools = []*ToolDefinition{{Name: "view", Input: ToolInput{jsonSchema: []byte(strings.Repeat(" ", maxToolSchemaBytes+1))}}}
			case "role":
				request.Messages[0].Role = ConversationRoleAssistant
			case "malformed":
				request.Messages[0].Parts[0] = ImageSourcePart{SourceKind: "fixture", Data: []byte(`null`)}
			}
			_, err = client.CountTokens(ctx, request)
			require.Error(t, err)
			assert.Zero(t, reads)
			assert.Empty(t, provider.requests)
		})
	}
}

func TestImageSourceAllowanceChargesEveryOccurrenceAndFixedInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	provider := &imageInputProvider{t: t}
	client, err := NewClient(provider)
	require.NoError(t, err)
	part := ImagePart{Format: ImageFormatPNG, Bytes: imageSourcePNG(t)}
	var allowances []int64
	client, err = WithImageSourceResolver(client, func(_ context.Context, _ ImageSourcePart, remaining int64) (ImagePart, error) {
		allowances = append(allowances, remaining)
		return part, nil
	})
	require.NoError(t, err)
	source := ImageSourcePart{SourceKind: "fixture", Data: []byte(`{}`)}
	request := &Request{Model: "fixture", Messages: []*Message{{Role: ConversationRoleUser, Parts: []Part{
		TextPart{Text: "fixed"}, source, source, part,
	}}}}
	walk := &dynamicValueWalk{}
	require.NoError(t, preflightRequestWithWalk(request, walk))
	expected := int64(maxDynamicValueBytes - walk.bytes + 2*(len(source.SourceKind)+len(source.Data)))
	_, err = client.CountTokens(ctx, request)
	require.NoError(t, err)
	assert.Equal(t, []int64{expected, expected - int64(len(part.Format)+len(part.Bytes))}, allowances)
}

func TestImageSourceErrorsAndCancellationDoNotBecomeCapacity(t *testing.T) {
	for _, sentinel := range []error{errors.New("authorization denied"), errors.New("checksum"), errors.New("missing"), errors.New("transport"), ErrImageSourceCapacity} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			provider := &imageInputProvider{t: t}
			client, err := NewClient(provider)
			require.NoError(t, err)
			client, err = WithImageSourceResolver(client, func(context.Context, ImageSourcePart, int64) (ImagePart, error) {
				return ImagePart{}, sentinel
			})
			require.NoError(t, err)
			_, err = client.Complete(ctx, &Request{Messages: []*Message{{Role: ConversationRoleUser, Parts: []Part{
				ImageSourcePart{SourceKind: "fixture", Data: []byte(`{}`)},
			}}}})
			require.ErrorIs(t, err, sentinel)
			assert.Equal(t, errors.Is(sentinel, ErrImageSourceCapacity), errors.Is(err, ErrImageSourceCapacity))
			assert.Empty(t, provider.requests)
		})
	}
}

func TestImageSourceCanonicalCodecAndProviderRejection(t *testing.T) {
	message := Message{Role: ConversationRoleUser, Parts: []Part{
		ImageSourcePart{SourceKind: "fixture", Data: []byte(`{"id":"one"}`)},
	}}
	data, err := json.Marshal(message)
	require.NoError(t, err)
	var decoded Message
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, message, decoded)
	_, err = NewRequestContract(&Request{Messages: []*Message{&decoded}})
	require.ErrorContains(t, err, "unresolved")
	err = ValidateResponse(&Response{Content: []Message{{Role: ConversationRoleAssistant, Parts: decoded.Parts}}, StopReason: "stop"})
	require.Error(t, err)
	for _, invalid := range []string{
		`{"kind":"image_source","source_kind":"fixture","data":{ },"extra":1}`,
		`{"kind":"image_source","source_kind":"","data":{}}`,
		`{"kind":"image_source","source_kind":"fixture","data":null}`,
		`{"kind":"image_source","source_kind":"fixture"}`,
	} {
		_, err := decodeMessagePart([]byte(invalid))
		require.Error(t, err)
	}
}

func TestImageSourceCandidateCapacityIsCheckedBeforeOwnerRead(t *testing.T) {
	for _, overreturn := range []bool{false, true} {
		t.Run(map[bool]string{false: "known refusal", true: "reader contract violation"}[overreturn], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			provider := &imageInputProvider{t: t}
			client, err := NewClient(provider)
			require.NoError(t, err)
			image := ImagePart{Format: ImageFormatPNG, Bytes: imageSourcePNG(t)}
			imageBytes := len(image.Format) + len(image.Bytes)
			source := ImageSourcePart{SourceKind: "fixture", Data: []byte(`{}`)}
			request := &Request{Messages: []*Message{{Role: ConversationRoleUser, Parts: []Part{
				source, source, TextPart{},
			}}}}
			walk := &dynamicValueWalk{}
			require.NoError(t, preflightRequestWithWalk(request, walk))
			// Derive pressure from the real canonical allowance: one image and
			// one spare byte fit, while both small descriptors initially fit.
			fixed := maxDynamicValueBytes - walk.bytes + 2*(len(source.SourceKind)+len(source.Data)) - imageBytes - 1
			request.Messages[0].Parts[2] = TextPart{Text: strings.Repeat("x", fixed)}
			ownerReads := 0
			var allowances []int64
			client, err = WithImageSourceResolver(client, func(_ context.Context, _ ImageSourcePart, remaining int64) (ImagePart, error) {
				allowances = append(allowances, remaining)
				if int64(imageBytes) > remaining && !overreturn {
					return ImagePart{}, ErrImageSourceCapacity
				}
				ownerReads++
				return image, nil
			})
			require.NoError(t, err)
			_, err = client.CountTokens(ctx, request)
			if overreturn {
				require.ErrorContains(t, err, "violated its byte allowance")
				require.NotErrorIs(t, err, ErrImageSourceCapacity)
				assert.Equal(t, 2, ownerReads)
			} else {
				require.ErrorIs(t, err, ErrImageSourceCapacity)
				assert.Equal(t, 1, ownerReads, "second known image is refused before owner I/O")
			}
			assert.Equal(t, []int64{int64(imageBytes + 1), 1}, allowances)
			assert.Empty(t, provider.requests)
			assert.Equal(t, source, request.Messages[0].Parts[0])
			assert.Equal(t, source, request.Messages[0].Parts[1])
		})
	}
}

func imageSourcePNG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	require.NoError(t, png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))))
	return data.Bytes()
}
