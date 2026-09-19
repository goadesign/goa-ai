package registry

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

func TestCatalogSpanPreservesFailuresRacingCancellation(t *testing.T) {
	storageErr := errors.New("catalog connection lost")
	for _, test := range []struct {
		name     string
		canceled bool
		err      error
		status   codes.Code
	}{
		{name: "success", status: codes.Ok},
		{name: "canceled success", canceled: true, status: codes.Ok},
		{name: "caller cancellation", canceled: true, err: fmt.Errorf("read: %w", context.Canceled), status: codes.Unset},
		{name: "caller deadline", canceled: true, err: context.DeadlineExceeded, status: codes.Unset},
		{name: "dependency deadline", err: context.DeadlineExceeded, status: codes.Error},
		{name: "storage error", err: storageErr, status: codes.Error},
		{name: "storage error during cancellation", canceled: true, err: storageErr, status: codes.Error},
		{name: "joined storage and cancellation", canceled: true, err: fmt.Errorf("read: %w", errors.Join(storageErr, context.Canceled)), status: codes.Error},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := newHealthSpanRecorder(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.canceled {
				cancel()
			}
			ctx, span := otel.Tracer("catalog-test").Start(ctx, "catalog-operation")
			finishCatalogSpan(ctx, span, &test.err)
			spans := recorder.Ended()
			require.Len(t, spans, 1)
			assert.Equal(t, test.status, spans[0].Status().Code)
			if test.status == codes.Error {
				require.Len(t, spans[0].Events(), 1)
				assert.Equal(t, "exception", spans[0].Events()[0].Name)
			} else {
				assert.Empty(t, spans[0].Events())
			}
		})
	}
}
