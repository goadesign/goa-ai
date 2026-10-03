package typesafe

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/eval"
	gentypesafe "goa.design/goa-ai/features/eval/typesafe/internal/gen/typesafe"
)

type (
	roundTripFunc func(*http.Request) (*http.Response, error)
	failedClose   struct {
		io.Reader
	}
)

const nativeResponse = `{"model":"jev-1.13.0","answers":{"required":{"type":"choice","choice":"a","probabilities":{"a":0.97,"b":0.01,"c":0.01,"d":0.01},"confidence":0.96}},"usage":{"input_tokens":123,"output_tokens":0}}`

func TestClientUsesNativeChoiceAndRetainsProbabilities(t *testing.T) {
	var calls int
	client, err := New(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		assert.Equal(t, endpoint, request.URL.String())
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "Bearer synthetic-key", request.Header.Get("Authorization"))
		data, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		require.NoError(t, request.Body.Close())
		decoded, err := gentypesafe.DecodeRequest(data)
		require.NoError(t, err)
		assert.Equal(t, gentypesafe.ModelVersion("jev-1.13.0"), decoded.Model)
		assert.Equal(t, "The task is complete.", decoded.State.Subject)
		assert.Equal(t, "The task was requested.", decoded.State.Reference)
		require.Contains(t, decoded.Questions, "required")
		question := decoded.Questions["required"]
		assert.Equal(t, "choice", question.Type)
		assert.Equal(t, "The task was completed.", question.Instructions.Requirement)
		assert.Equal(t, classificationInstructions, question.Instructions.Contract)
		assert.Contains(t, question.Criteria.A, "establishes the requirement")
		return httpResponse(http.StatusOK, nativeResponse), nil
	})}, testConfig())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, err := client.Classify(ctx, "The task is complete.", testClaims(), "The task was requested.")
	require.NoError(t, err)
	require.Len(t, result.Predictions, 1)
	assert.Equal(t, eval.Entailed, result.Predictions[0].Label)
	assert.Equal(t, map[eval.Label]float64{
		eval.Entailed: .97, eval.Contradicted: .01, eval.NotAddressed: .01, eval.Indeterminate: .01,
	}, result.Predictions[0].Probabilities)
	require.Len(t, result.Calls, 1)
	require.NotNil(t, result.Calls[0].Usage)
	assert.Equal(t, 123, result.Calls[0].Usage.InputTokens)
	assert.Equal(t, 0, result.Calls[0].Usage.OutputTokens)
	assert.Equal(t, 123, result.Calls[0].Usage.TotalTokens)
	assert.Equal(t, "jev-1.13.0", result.Calls[0].Usage.Model)
	assert.Equal(t, 1, calls)
	assert.NotContains(t, client.Config().Instructions, "synthetic-key")
}

func TestClientRejectsInvalidNativeResponsesWithoutRetry(t *testing.T) {
	tests := []struct {
		name     string
		response string
		status   int
		want     string
	}{
		{"changed model", strings.Replace(nativeResponse, "jev-1.13.0", "jev-1.14.0", 1), 200, "returned model"},
		{"missing answer", strings.Replace(nativeResponse, `"required"`, `"different"`, 1), 200, "omitted answer"},
		{"invalid sum", strings.Replace(nativeResponse, `"a":0.97`, `"a":0.9`, 1), 200, "sum"},
		{"wrong winner", strings.Replace(nativeResponse, `"choice":"a"`, `"choice":"b"`, 1), 200, "less probable"},
		{"unknown option", strings.Replace(nativeResponse, `"choice":"a"`, `"choice":"e"`, 1), 200, "choice"},
		{"missing probability", strings.Replace(nativeResponse, `,"d":0.01`, "", 1), 200, "d"},
		{"duplicate probability", strings.Replace(nativeResponse, `"a":0.97`, `"a":0.1,"a":0.97`, 1), 200, "duplicate"},
		{"extra probability", strings.Replace(nativeResponse, `"a":0.97`, `"e":0,"a":0.97`, 1), 200, "e"},
		{"out of range", strings.Replace(nativeResponse, `"a":0.97`, `"a":1.1`, 1), 200, "a"},
		{"null probabilities", strings.Replace(nativeResponse, `{"a":0.97,"b":0.01,"c":0.01,"d":0.01}`, "null", 1), 200, "probabilities"},
		{"negative usage", strings.Replace(nativeResponse, `"input_tokens":123`, `"input_tokens":-1`, 1), 200, "input_tokens"},
		{"missing model", strings.Replace(nativeResponse, `"model":"jev-1.13.0",`, "", 1), 200, "model"},
		{"unknown field", strings.Replace(nativeResponse, `{"model"`, `{"unexpected":true,"model"`, 1), 200, "unexpected"},
		{"unauthorized", `{}`, 401, "HTTP 401"},
		{"rate limited", `{}`, 429, "HTTP 429"},
		{"overloaded", `{}`, 529, "HTTP 529"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls int
			client, err := New(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				calls++
				return httpResponse(test.status, test.response), nil
			})}, testConfig())
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			result, err := client.Classify(ctx, "The task is complete.", testClaims(), "")
			require.ErrorContains(t, err, test.want)
			require.Len(t, result.Calls, 1)
			assert.NotEmpty(t, result.Calls[0].Error)
			assert.Equal(t, 1, calls)
		})
	}
}

func TestResponseCeilingAppliesToEachResponse(t *testing.T) {
	for _, extra := range []int64{-1, 0, 1} {
		t.Run("ceiling_offset_"+strconv.FormatInt(extra, 10), func(t *testing.T) {
			config := testConfig()
			config.MaxResponseBytes = int64(len(nativeResponse)) + extra
			client, err := New(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return httpResponse(200, nativeResponse), nil
			})}, config)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			for range 2 {
				_, err := client.Classify(ctx, "The task is complete.", testClaims(), "")
				if extra < 0 {
					assert.ErrorContains(t, err, "exceeds")
				} else {
					assert.NoError(t, err)
				}
			}
		})
	}
}

func TestClientCancellationAndTransportErrors(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		client, err := New(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		})}, testConfig())
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		defer cancel()
		result, err := client.Classify(ctx, "Captured.", testClaims(), "")
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Len(t, result.Calls, 1)
		assert.Nil(t, result.Calls[0].Usage)
	})
	t.Run("close error", func(t *testing.T) {
		client, err := New(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: failedClose{Reader: strings.NewReader(nativeResponse)}}, nil
		})}, testConfig())
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		_, err = client.Classify(ctx, "Captured.", testClaims(), "")
		assert.ErrorContains(t, err, "synthetic close failure")
	})
}

func TestClientRequiresPinnedVersionAndBoundedRequests(t *testing.T) {
	for _, version := range []string{"", "jev-latest", "jev-preview", "jev-1.13"} {
		config := testConfig()
		config.Model = version
		_, err := New(http.DefaultClient, config)
		require.ErrorContains(t, err, "pinned model")
	}
	for _, limit := range []int64{0, -1, math.MaxInt64} {
		config := testConfig()
		config.MaxResponseBytes = limit
		_, err := New(http.DefaultClient, config)
		require.ErrorContains(t, err, "byte ceiling")
	}
	client, err := New(http.DefaultClient, testConfig())
	require.NoError(t, err)
	result, err := client.Classify(context.Background(), "Captured.", testClaims(), "")
	require.ErrorContains(t, err, "deadline")
	assert.Empty(t, result.Calls)
}

func TestChoiceOrderChangesBothQuestionsAndQualificationIdentity(t *testing.T) {
	config := testConfig()
	config.ChoiceOrder = []eval.Label{eval.Indeterminate, eval.NotAddressed, eval.Contradicted, eval.Entailed}
	client, err := New(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		require.NoError(t, request.Body.Close())
		decoded, err := gentypesafe.DecodeRequest(data)
		require.NoError(t, err)
		assert.Contains(t, decoded.Questions["required"].Criteria.D, "establishes the requirement")
		response := strings.Replace(nativeResponse, `"choice":"a"`, `"choice":"d"`, 1)
		response = strings.Replace(response, `"a":0.97`, `"a":0.01`, 1)
		response = strings.Replace(response, `"d":0.01`, `"d":0.97`, 1)
		return httpResponse(200, response), nil
	})}, config)
	require.NoError(t, err)
	standard, err := New(http.DefaultClient, testConfig())
	require.NoError(t, err)
	assert.NotEqual(t, standard.Config(), client.Config())
	assert.Equal(t, string(eval.Entailed), client.Config().Settings["d"])
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, err := client.Classify(ctx, "Captured.", testClaims(), "")
	require.NoError(t, err)
	assert.Equal(t, eval.Entailed, result.Predictions[0].Label)
	assert.InDelta(t, .97, result.Predictions[0].Probabilities[eval.Entailed], 1e-12)
	config.ChoiceOrder[0] = eval.Entailed
	_, err = New(http.DefaultClient, config)
	assert.ErrorContains(t, err, "exactly once")
}

func TestMissingUsageIsUnknown(t *testing.T) {
	client, err := New(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return httpResponse(200, strings.Replace(nativeResponse, `"input_tokens":123,"output_tokens":0`, `"input_tokens":123`, 1)), nil
	})}, testConfig())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, err := client.Classify(ctx, "Captured.", testClaims(), "")
	require.NoError(t, err)
	assert.Nil(t, result.Calls[0].Usage)
}

func testConfig() Config {
	return Config{APIKey: "synthetic-key", Model: "jev-1.13.0", MaxResponseBytes: 4096}
}

func testClaims() []eval.Claim {
	return []eval.Claim{{ID: "required", Text: "The task was completed."}}
}

func httpResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (failedClose) Close() error {
	return errors.New("synthetic close failure")
}
