package typesafe

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gentypesafe "goa.design/goa-ai/features/eval/typesafe/internal/gen/typesafe"
)

const noulResponse = `{"model":"jev-1.13.0","answers":{"required":{"type":"noul","noul":0.97}},"usage":{"input_tokens":123,"output_tokens":5}}`

func TestNoulUsesBinaryCriterionWithoutInventingFailureLabels(t *testing.T) {
	var calls int
	client, err := NewNoul(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		data, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		require.NoError(t, request.Body.Close())
		decoded, err := gentypesafe.DecodeNoulRequest(data)
		require.NoError(t, err)
		assert.Equal(t, "Captured answer.", decoded.State.Subject)
		assert.Equal(t, "Captured facts.", decoded.State.Reference)
		question := decoded.Questions["required"]
		assert.Equal(t, "noul", question.Type)
		assert.Equal(t, binaryQuestion, question.Instructions.Question)
		assert.Equal(t, testClaims()[0].Text, question.Instructions.Requirement)
		assert.Equal(t, binaryFalse, question.Criteria.False)
		return httpResponse(200, noulResponse), nil
	})}, testConfig())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result, err := client.Classify(ctx, "Captured answer.", testClaims(), "Captured facts.")
	require.NoError(t, err)
	assert.InDelta(t, .97, result.Predictions[0].Probability, 0)
	require.Len(t, result.Calls, 1)
	assert.True(t, bytes.Equal([]byte(noulResponse), result.Calls[0].Response), "retain the exact provider response bytes")
	assert.Equal(t, 128, result.Calls[0].Usage.TotalTokens)
	assert.Equal(t, 1, calls)
	choice, err := NewChoice(http.DefaultClient, testConfig())
	require.NoError(t, err)
	assert.NotEqual(t, choice.Config(), client.Config())
}

func TestNoulRejectsProtocolErrorsWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{"missing probability", strings.Replace(noulResponse, `,"noul":0.97`, "", 1), "noul"},
		{"out of range", strings.Replace(noulResponse, `"noul":0.97`, `"noul":1.1`, 1), "noul"},
		{"duplicate probability", strings.Replace(noulResponse, `"noul":0.97`, `"noul":0.2,"noul":0.97`, 1), "duplicate"},
		{"wrong primitive", strings.Replace(noulResponse, `"type":"noul"`, `"type":"choice"`, 1), "type"},
		{"wrong model", strings.Replace(noulResponse, "jev-1.13.0", "jev-1.14.0", 1), "returned model"},
		{"wrong identity", strings.Replace(noulResponse, `"required"`, `"different"`, 1), "omitted answer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls int
			client, err := NewNoul(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return httpResponse(200, test.body), nil
			})}, testConfig())
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			result, err := client.Classify(ctx, "Captured.", testClaims(), "")
			require.ErrorContains(t, err, test.want)
			require.Len(t, result.Calls, 1)
			assert.NotEmpty(t, result.Calls[0].Error)
			assert.Equal(t, 1, calls)
		})
	}
}
