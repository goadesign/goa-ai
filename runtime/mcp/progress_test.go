// These tests verify progress at the protocol boundary. Tokens and ordering
// belong to one request; arbitrary finite work values remain service-owned.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProgressTokens(t *testing.T) {
	for _, test := range []struct{ raw, key string }{
		{`""`, "string:"}, {`" a "`, "string: a "}, {`9007199254740993`, "integer:9007199254740993"}, {`1.0`, "integer:1"}, {`1e2`, "integer:100"}, {`-1`, "integer:-1"},
	} {
		t.Run(test.raw, func(t *testing.T) {
			key, err := progressTokenKey(json.RawMessage(test.raw))
			require.NoError(t, err)
			assert.Equal(t, test.key, key)
		})
	}
	for _, raw := range []string{`null`, `true`, `{}`, `[]`, `1.5`, ``, `"broken`} {
		t.Run(raw, func(t *testing.T) { _, err := progressTokenKey(json.RawMessage(raw)); require.Error(t, err) })
	}
}

func TestProgressNotificationValidation(t *testing.T) {
	for _, params := range []string{
		`{}`, `null`, `[]`, `{"progressToken":"t"}`, `{"progressToken":null,"progress":0}`, `{"progressToken":"t","progress":null}`,
		`{"progressToken":"t","progress":"0"}`, `{"progressToken":"t","progress":0,"total":null}`, `{"progressToken":"t","progress":0,"message":null}`,
		`{"progressToken":"t","progress":0,"_meta":[]}`, `{"progressToken":"other","progress":0}`, `{"progressToken":1.1,"progress":0}`,
	} {
		t.Run(params, func(t *testing.T) {
			receiver := &progressReceiver{token: "string:t", requestID: json.RawMessage(`"request"`)}
			err := receiver.notification(t.Context(), rpcMessage{Method: "notifications/progress", Params: json.RawMessage(params)})
			var malformed *MalformedResponseError
			require.ErrorAs(t, err, &malformed)
		})
	}
	updates := []Progress{}
	ctx := WithProgress(t.Context(), func(_ context.Context, update Progress) error { updates = append(updates, update); return nil })
	meta := map[string]json.RawMessage{}
	receiver, err := newProgressReceiver(ctx, json.RawMessage(`"request"`), meta)
	require.NoError(t, err)
	for _, value := range []float64{-10.5, 0, 200.25} {
		raw, err := json.Marshal(progressWire{Token: meta["progressToken"], Value: &value, Total: new(float64(100)), Message: new("")})
		require.NoError(t, err)
		require.NoError(t, receiver.notification(ctx, rpcMessage{Method: "notifications/progress", Params: raw}))
	}
	require.Len(t, updates, 3)
	assert.JSONEq(t, `"request"`, string(updates[0].RequestID))
	assert.Equal(t, new(""), updates[0].Message)
	for _, value := range []float64{200.25, -1} {
		err := receiver.accept(ctx, &progressWire{Token: meta["progressToken"], Value: &value})
		var malformed *MalformedResponseError
		require.ErrorAs(t, err, &malformed)
	}
	assert.Len(t, updates, 3)
	require.NoError(t, receiver.notification(ctx, rpcMessage{Method: "notifications/extension", Params: json.RawMessage(`{}`)}))
}

func TestProgressValuesAndHandlerFailure(t *testing.T) {
	for _, value := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
		require.Error(t, validateProgressValues(value, nil))
		require.Error(t, validateProgressValues(0, &value))
	}
	failure := errors.New("host stopped")
	receiver := &progressReceiver{token: "string:t", handler: func(context.Context, Progress) error { return failure }}
	require.ErrorIs(t, receiver.accept(t.Context(), &progressWire{Token: json.RawMessage(`"t"`), Value: new(float64(0))}), failure)
	require.NoError(t, ReportProgress(t.Context(), 0, nil, nil))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, ReportProgress(ctx, 0, nil, nil), context.Canceled)
}
