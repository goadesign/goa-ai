package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
)

func TestShouldRecordSpanError(t *testing.T) {
	t.Parallel()

	doneCanceled := canceledContext(t)
	doneDeadline := expiredContext(t)
	liveCtx := context.Background()
	boom := errors.New("boom")

	testCases := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{
			name: "nil error does not record",
			ctx:  liveCtx,
			err:  nil,
			want: false,
		},
		{
			name: "live context records canceled sentinel",
			ctx:  liveCtx,
			err:  context.Canceled,
			want: true,
		},
		{
			name: "live context records deadline sentinel",
			ctx:  liveCtx,
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "done canceled context suppresses canceled sentinel",
			ctx:  doneCanceled,
			err:  context.Canceled,
			want: false,
		},
		{
			name: "done deadline context suppresses deadline sentinel",
			ctx:  doneDeadline,
			err:  context.DeadlineExceeded,
			want: false,
		},
		{
			name: "done context suppresses grpc canceled",
			ctx:  doneCanceled,
			err:  grpcStatus.Error(grpcCodes.Canceled, "context canceled"),
			want: false,
		},
		{
			name: "done context suppresses grpc deadline exceeded",
			ctx:  doneDeadline,
			err:  grpcStatus.Error(grpcCodes.DeadlineExceeded, "context deadline exceeded"),
			want: false,
		},
		{
			name: "live context records grpc canceled",
			ctx:  liveCtx,
			err:  grpcStatus.Error(grpcCodes.Canceled, "context canceled"),
			want: true,
		},
		{
			name: "live context records grpc deadline exceeded",
			ctx:  liveCtx,
			err:  grpcStatus.Error(grpcCodes.DeadlineExceeded, "context deadline exceeded"),
			want: true,
		},
		{
			name: "done context still records unrelated error",
			ctx:  doneCanceled,
			err:  boom,
			want: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, ShouldRecordSpanError(tc.ctx, tc.err))
		})
	}
}

// canceledContext returns a context whose cancellation is already observable.
func canceledContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// expiredContext returns a context whose deadline is already observable.
func expiredContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)
	return ctx
}
