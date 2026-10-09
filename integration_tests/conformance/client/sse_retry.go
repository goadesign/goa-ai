// This file runs the production caller against an independent interrupted-stream
// peer. The host explicitly chooses endpoint trust and an attempt allowance;
// the peer checks actual requests and effects without importing Goa-AI code.
package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"os"

	"goa.design/goa-ai/runtime/mcp"
)

type (
	// sseRetryContext contains one local referee case's host policy and expected outcome.
	sseRetryContext struct {
		Outcome  string `json:"outcome"`
		Attempts int    `json:"attempts"`
		Trust    bool   `json:"trust"`
	}
)

// exerciseInterruptedSSE sends one domain operation with the fixture's explicit
// policy. It checks the returned value or typed error; the Node peer checks replay.
func exerciseInterruptedSSE(endpoint string) error {
	var configuration sseRetryContext
	if err := json.Unmarshal([]byte(os.Getenv("MCP_CONFORMANCE_CONTEXT")), &configuration); err != nil {
		return err
	}
	caller, err := mcp.NewHTTPCaller(mcp.HTTPOptions{
		Endpoint:   endpoint,
		ClientInfo: mcp.ClientInfo{Name: "goa-ai-conformance", Version: "1"},
		RetryPolicy: mcp.HTTPRetryPolicy{
			MaxAttempts:          configuration.Attempts,
			TrustToolAnnotations: configuration.Trust,
		},
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if configuration.Outcome == "cancelled" {
		ctx = mcp.WithProgress(ctx, func(context.Context, mcp.Progress) error {
			cancel()
			return nil
		})
	}
	request := mcp.CallRequest{Tool: "operation", Payload: []byte(`{"operation":"same"}`)}
	result, err := caller.CallTool(ctx, request)
	if configuration.Outcome == "rounds" {
		if err != nil {
			return err
		}
		if result.InputRequired == nil || result.InputRequired.RequestState == nil || len(result.InputRequired.Requests) != 0 {
			return errors.New("SSE peer did not return a state-only unfinished round")
		}
		request.Continuation = &mcp.CallContinuation{RequestState: result.InputRequired.RequestState}
		result, err = caller.CallTool(ctx, request)
	}
	switch configuration.Outcome {
	case "complete", "rounds":
		if err != nil {
			return err
		}
		if result.InputRequired != nil || string(result.StructuredContent) != "42" {
			return errors.New("SSE peer did not return the completed typed result")
		}
	case "unknown":
		var unknown *mcp.OutcomeUnknownError
		if !errors.As(err, &unknown) {
			return errors.New("stream loss did not retain an unknown tool outcome")
		}
	case "later-unauthorized":
		var unknown *mcp.OutcomeUnknownError
		var rejection *mcp.HTTPResponseError
		if !errors.As(err, &unknown) || !errors.As(err, &rejection) || rejection.StatusCode != http.StatusUnauthorized {
			return errors.New("later authorization rejection erased the lost response or HTTP status")
		}
	case "cancelled":
		if !errors.Is(err, context.Canceled) {
			return errors.New("SSE operation did not preserve host cancellation")
		}
	case "malformed":
		var malformed *mcp.MalformedResponseError
		if !errors.As(err, &malformed) {
			return errors.New("completed malformed event did not retain its typed error")
		}
	case "protocol-error":
		var rejected *mcp.Error
		if !errors.As(err, &rejected) || rejected.Code != mcp.JSONRPCInvalidParams {
			return errors.New("completed protocol error did not retain its code")
		}
	case "tool-error":
		var failed *mcp.ToolExecutionError
		if !errors.As(err, &failed) {
			return errors.New("completed tool error did not retain its typed result")
		}
	default:
		return errors.New("unsupported interrupted-SSE referee outcome")
	}
	return nil
}
