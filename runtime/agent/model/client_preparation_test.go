// Request preparation must precede the immutable model contract while preserving
// caller ownership, observers, counting, and each individual invocation's errors.
package model

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	preparationContextKey int

	preparationContextProvider struct {
		t      *testing.T
		parent context.Context
	}
)

func TestRequestPreparationContextReachesExactConcurrentCall(t *testing.T) {
	parent, cancel := context.WithDeadline(t.Context(), time.Now().Add(time.Minute))
	t.Cleanup(cancel)
	provider := &preparationContextProvider{t: t, parent: parent}
	base, err := NewClient(provider)
	require.NoError(t, err)
	var prepared sync.WaitGroup
	prepared.Add(4)
	client, err := WithRequestPreparation(base, func(ctx context.Context, req *Request) (context.Context, *Request, error) {
		ctx = context.WithValue(ctx, preparationContextKey(0), req.ModelClass)
		prepared.Done()
		prepared.Wait()
		return ctx, req, nil
	})
	require.NoError(t, err)
	client, err = WrapClient(client, func(provider Provider) Provider {
		return &clientTestProviderMiddleware{Provider: provider}
	})
	require.NoError(t, err)
	client, err = WithRequestPreparation(client, func(ctx context.Context, req *Request) (context.Context, *Request, error) {
		assert.Equal(t, req.ModelClass, ctx.Value(preparationContextKey(0)))
		return ctx, req, nil
	})
	require.NoError(t, err)
	var calls sync.WaitGroup
	for _, class := range []ModelClass{ModelClassSmall, ModelClassDefault} {
		for _, streaming := range []bool{false, true} {
			calls.Add(1)
			go func() {
				defer calls.Done()
				name := string(class) + "/" + map[bool]string{false: "complete", true: "stream"}[streaming]
				t.Run(name, func(t *testing.T) {
					req := structuredOutputRequest(`{"type":"object"}`)
					req.ModelClass = class
					if streaming {
						stream, err := client.Stream(parent, req)
						require.NoError(t, err)
						for {
							_, err := stream.Recv()
							if errors.Is(err, io.EOF) {
								break
							}
							require.NoError(t, err)
						}
						require.NoError(t, stream.Close())
					} else {
						_, err := client.Complete(parent, req)
						require.NoError(t, err)
					}
					assert.Nil(t, parent.Value(preparationContextKey(0)))
					assert.Nil(t, parent.Value(preparationContextKey(1)))
				})
			}()
		}
	}
	calls.Wait()
}

func TestRequestPreparationPrecedesContractAndObservers(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "stream"}[streaming], func(t *testing.T) {
			provider := &clientTestContractProvider{}
			var events []string
			observer := &observerTestPreparer{
				name: "final request", events: &events,
				call: &observerTestCall{name: "final request", events: &events},
				mutate: func(request *Request) {
					assert.JSONEq(t, `{"type":"object"}`, string(request.StructuredOutput.Schema))
					assert.Equal(t, "prepared", request.Messages[0].Parts[0].(TextPart).Text)
				},
			}
			client, err := newValidatedClient(provider, provider, []ProviderCallObserver{observer})
			require.NoError(t, err)
			var callbackRequest *Request
			client, err = WithRequestPreparation(client, func(ctx context.Context, request *Request) (context.Context, *Request, error) {
				events = append(events, "transform")
				request.StructuredOutput.Schema = []byte(`{"type":"object"}`)
				request.Messages[0].Parts[0] = TextPart{Text: "prepared"}
				callbackRequest = request
				return ctx, request, nil
			})
			require.NoError(t, err)
			client, err = WrapClient(client, func(provider Provider) Provider {
				return &clientTestProviderMiddleware{Provider: provider}
			})
			require.NoError(t, err)
			request := structuredOutputRequest(`{"type":"array"}`)
			request.Messages = []*Message{{Role: ConversationRoleUser, Parts: []Part{TextPart{Text: "original"}}}}
			if streaming {
				stream, err := client.Stream(t.Context(), request)
				require.NoError(t, err)
				for {
					_, err := stream.Recv()
					if errors.Is(err, io.EOF) {
						break
					}
					require.NoError(t, err)
				}
				require.NoError(t, stream.Close())
			} else {
				_, err := client.Complete(t.Context(), request)
				require.NoError(t, err)
			}
			assert.Equal(t, "transform", events[0])
			assert.Equal(t, "prepare final request", events[1])
			assert.JSONEq(t, `{"type":"array"}`, string(request.StructuredOutput.Schema))
			assert.Equal(t, "original", request.Messages[0].Parts[0].(TextPart).Text)
			assert.Nil(t, callbackRequest.preparedContract)
		})
	}
}

func TestRequestPreparationIsPerCallAndDoesNotRunForCounting(t *testing.T) {
	provider := &clientTestCountingProvider{count: TokenCount{Model: "destination", ModelClass: ModelClassDefault, InputTokens: 7, Exact: true}}
	base, err := NewClient(provider)
	require.NoError(t, err)
	var classes []ModelClass
	client, err := WithRequestPreparation(base, func(ctx context.Context, request *Request) (context.Context, *Request, error) {
		classes = append(classes, request.ModelClass)
		return ctx, request, nil
	})
	require.NoError(t, err)
	_, err = client.CountTokens(t.Context(), &Request{ModelClass: ModelClassDefault})
	require.NoError(t, err)
	assert.Empty(t, classes)
	assert.Equal(t, 1, provider.countCalls)
	for _, class := range []ModelClass{ModelClassSmall, ModelClassDefault} {
		_, err := client.Complete(t.Context(), &Request{ModelClass: class})
		require.NoError(t, err)
	}
	assert.Equal(t, []ModelClass{ModelClassSmall, ModelClassDefault}, classes)
	_, err = base.Complete(t.Context(), &Request{})
	require.NoError(t, err)
	assert.Len(t, classes, 2, "registering preparation must not change the base client")
}

func TestRequestPreparationRejectsInvalidInputAndOutput(t *testing.T) {
	failure := errors.New("counting failed")
	tests := []struct {
		name      string
		request   *Request
		prepare   func(context.Context, *Request) (context.Context, *Request, error)
		wantCalls int
		wantError error
	}{
		{name: "invalid original", request: &Request{Tools: []*ToolDefinition{{}}}, prepare: func(ctx context.Context, request *Request) (context.Context, *Request, error) {
			request.Tools = nil
			return ctx, request, nil
		}},
		{name: "invalid transformed", request: &Request{}, wantCalls: 1, prepare: func(ctx context.Context, request *Request) (context.Context, *Request, error) {
			request.Tools = []*ToolDefinition{{}}
			return ctx, request, nil
		}},
		{name: "nil transformed", request: &Request{}, wantCalls: 1, prepare: func(ctx context.Context, _ *Request) (context.Context, *Request, error) {
			return ctx, nil, nil
		}},
		{name: "nil context", request: &Request{}, wantCalls: 1, prepare: func(_ context.Context, req *Request) (context.Context, *Request, error) {
			return nil, req, nil
		}},
		{name: "canceled context", request: &Request{}, wantCalls: 1, wantError: context.Canceled, prepare: func(ctx context.Context, req *Request) (context.Context, *Request, error) {
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			return ctx, req, nil
		}},
		{name: "error", request: &Request{}, wantCalls: 1, wantError: failure, prepare: func(ctx context.Context, _ *Request) (context.Context, *Request, error) {
			return ctx, nil, failure
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &clientTestProvider{}
			base, err := NewClient(provider)
			require.NoError(t, err)
			calls := 0
			client, err := WithRequestPreparation(base, func(ctx context.Context, request *Request) (context.Context, *Request, error) {
				calls++
				return test.prepare(ctx, request)
			})
			require.NoError(t, err)
			_, err = client.Complete(t.Context(), test.request)
			require.Error(t, err)
			if test.wantError != nil {
				assert.ErrorIs(t, err, test.wantError)
			}
			assert.Equal(t, test.wantCalls, calls)
			assert.Zero(t, provider.calls)
		})
	}
}

func TestRequestPreparationRequiresValidConfiguration(t *testing.T) {
	base, err := NewClient(&clientTestProvider{})
	require.NoError(t, err)
	_, err = WithRequestPreparation(base, nil)
	assert.EqualError(t, err, "model request preparation is required")
	_, err = WithRequestPreparation(nil, func(ctx context.Context, request *Request) (context.Context, *Request, error) {
		return ctx, request, nil
	})
	assert.Error(t, err)
}

func (p *preparationContextProvider) Complete(ctx context.Context, req *Request) (*Response, error) {
	p.checkContext(ctx, req)
	assert.Equal(p.t, true, ctx.Value(preparationContextKey(1)))
	return (&clientTestContractProvider{}).Complete(ctx, req)
}

func (p *preparationContextProvider) Stream(ctx context.Context, req *Request) (Streamer, error) {
	p.checkContext(ctx, req)
	assert.Equal(p.t, true, ctx.Value(preparationContextKey(1)))
	return (&clientTestContractProvider{}).Stream(ctx, req)
}

func (p *preparationContextProvider) PrepareClientCall(ctx context.Context, req *Request) (context.Context, ClientCallObserver, error) {
	p.checkContext(ctx, req)
	return context.WithValue(ctx, preparationContextKey(1), true), nil, nil
}

func (p *preparationContextProvider) checkContext(ctx context.Context, req *Request) {
	p.t.Helper()
	assert.Equal(p.t, req.ModelClass, ctx.Value(preparationContextKey(0)))
	wantDeadline, wantOK := p.parent.Deadline()
	deadline, ok := ctx.Deadline()
	assert.Equal(p.t, wantOK, ok)
	assert.Equal(p.t, wantDeadline, deadline)
}
