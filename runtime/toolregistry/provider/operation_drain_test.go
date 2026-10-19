// Planned shutdown stops intake while accepted work still owns its operation
// contexts. An operation's own deadline must remain a settlement failure even
// when intake was canceled first; only completed settlement permits release.
package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
)

func TestOperationSettlementDuringDrain(t *testing.T) {
	for _, phase := range []string{testPhaseClaim, testPhaseComplete, testPhaseAck} {
		for _, expires := range []bool{false, true} {
			outcome := "completes"
			if expires {
				outcome = "times out"
			}
			t.Run(phase+"/"+outcome, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					entered := make(chan struct{})
					settle := func(operationCtx context.Context) error {
						close(entered)
						if expires {
							<-operationCtx.Done()
							return operationCtx.Err()
						}
						time.Sleep(10 * time.Millisecond)
						return operationCtx.Err()
					}
					var releases atomic.Int64
					registration := successfulRegistration()
					registration.Release = func(context.Context, string, string, string, string) error {
						releases.Add(1)
						return nil
					}
					registration.Claim = func(ctx context.Context, _ ClaimRequest) (ClaimDisposition, error) {
						if phase == testPhaseClaim {
							if err := settle(ctx); err != nil {
								return "", err
							}
						}
						return ClaimExecute, nil
					}
					registration.Complete = func(ctx context.Context, _, _, _, _, _ string, _ toolregistry.ToolResultMessage) (bool, error) {
						if phase == testPhaseComplete {
							if err := settle(ctx); err != nil {
								return false, err
							}
						}
						return true, nil
					}
					events := make(chan *streaming.Event, 1)
					events <- testToolCallEvent(t, "accepted-call")
					sink := mockpulse.NewSink(t)
					sink.SetSubscribe(func() <-chan *streaming.Event { return events })
					sink.SetClose(func(context.Context) error { return nil })
					sink.SetAck(func(ctx context.Context, _ *streaming.Event) error {
						if phase == testPhaseAck {
							return settle(ctx)
						}
						return nil
					})
					stream := mockpulse.NewStream(t)
					stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) { return sink, nil })
					client := mockpulse.NewClient(t)
					client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) { return stream, nil })
					done := make(chan error, 1)
					go func() {
						done <- ServeAndWait(ctx, client, "test.toolset", &recordingHandler{}, registration, Options{
							ProviderID: testProviderID, Pong: func(context.Context, string, string, string) error { return nil },
							OperationTimeout: 20 * time.Millisecond, ShutdownTimeout: time.Second, EnsureInterval: time.Hour,
						})
					}()
					<-entered
					cancel()
					err := <-done
					require.ErrorIs(t, err, context.Canceled)
					if expires {
						require.ErrorIs(t, err, context.DeadlineExceeded)
						assert.Zero(t, releases.Load(), "failed settlement must retain its lease for expiry")
					} else {
						require.NotErrorIs(t, err, context.DeadlineExceeded)
						assert.EqualValues(t, 1, releases.Load())
					}
				})
			})
		}
	}
}
