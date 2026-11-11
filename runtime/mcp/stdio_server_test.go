// These tests exercise the native stdio MCP server transport with real OS
// pipes, subprocesses, StdioCaller, concurrent requests, progress updates,
// subscriptions, legacy 2025-11-25 initialization and cancellation, EOF with
// active context-waiting work, and subprocess shutdown under idle input and
// blocked output.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa/v3/jsonrpc"
)

func TestStdioServerValidationAndLegacyLifecycle(t *testing.T) {
	serverIn, clientOut, err := os.Pipe()
	require.NoError(t, err)
	clientIn, serverOut, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		if closeErr := clientOut.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close clientOut: %v", closeErr)
		}
		if closeErr := clientIn.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close clientIn: %v", closeErr)
		}
	})

	legacySlowStarted := make(chan struct{})
	legacySlowCancelled := make(chan error, 1)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- ServeStdio(t.Context(), serverIn, serverOut, func(ctx context.Context, req *jsonrpc.RawRequest, legacy bool) *jsonrpc.Response {
			switch req.Method {
			case methodInitialize:
				return jsonrpc.MakeSuccessResponse(req.ID, map[string]any{
					"protocolVersion": LegacyProtocolVersion,
					"capabilities":    map[string]any{},
					"serverInfo":      map[string]any{"name": "stdio-test", "version": "1"},
				})
			case "ping":
				return jsonrpc.MakeSuccessResponse(req.ID, map[string]any{})
			case "tools/list":
				return jsonrpc.MakeSuccessResponse(req.ID, map[string]any{"legacy": legacy})
			case methodToolsCall:
				close(legacySlowStarted)
				<-ctx.Done()
				legacySlowCancelled <- ctx.Err()
				return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InternalError, ctx.Err().Error(), nil)
			default:
				return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.MethodNotFound, "Method not found", nil)
			}
		})
	}()

	reader := bufio.NewReader(clientIn)
	sendAndRead := func(line string) rpcMessage {
		t.Helper()
		_, writeErr := clientOut.WriteString(line + "\n")
		require.NoError(t, writeErr)
		raw, readErr := reader.ReadBytes('\n')
		require.NoError(t, readErr)
		var msg rpcMessage
		require.NoError(t, json.Unmarshal(raw, &msg))
		return msg
	}

	parseErr := sendAndRead("{invalid json")
	invalidReq := sendAndRead(`{"jsonrpc":"2.0","id":null,"method":"tools/list"}`)
	missingMeta := sendAndRead(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	unsupportedVer := sendAndRead(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01","io.modelcontextprotocol/clientCapabilities":{}}}}`)

	assert.Contains(t, string(parseErr.Error), "-32700")
	assert.Contains(t, string(invalidReq.Error), "-32600")
	assert.Contains(t, string(missingMeta.Error), "-32602")
	assert.Contains(t, string(unsupportedVer.Error), "-32022")

	initMsg := sendAndRead(`{"jsonrpc":"2.0","id":3,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"legacy-host","version":"1.0"}}}`)
	assert.Contains(t, string(initMsg.Result), LegacyProtocolVersion)

	_, err = clientOut.WriteString(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	require.NoError(t, err)
	_, err = clientOut.WriteString(`{"jsonrpc":"2.0","method":"notifications/example"}` + "\n")
	require.NoError(t, err)
	_, err = clientOut.WriteString(`{"jsonrpc":"2.0","method":"notifications/unknown","params":{"ignored":true}}` + "\n")
	require.NoError(t, err)

	pingMsg := sendAndRead(`{"jsonrpc":"2.0","id":4,"method":"ping"}`)
	assert.JSONEq(t, `4`, string(pingMsg.ID))
	assert.JSONEq(t, `{}`, string(pingMsg.Result))

	legacyListMsg := sendAndRead(`{"jsonrpc":"2.0","id":5,"method":"tools/list"}`)
	assert.JSONEq(t, `5`, string(legacyListMsg.ID))
	assert.JSONEq(t, `{"legacy":true}`, string(legacyListMsg.Result))

	// Legacy 2025-11-25 notifications/cancelled cancels an active legacy request.
	_, err = clientOut.WriteString(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"slow"}}` + "\n")
	require.NoError(t, err)
	select {
	case <-legacySlowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("legacy slow tools/call did not start")
	}
	_, err = clientOut.WriteString(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7,"reason":"legacy host abort"}}` + "\n")
	require.NoError(t, err)
	select {
	case cancelErr := <-legacySlowCancelled:
		require.ErrorIs(t, cancelErr, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("legacy notifications/cancelled did not cancel active request")
	}

	_, err = clientOut.WriteString(`{"jsonrpc":"2.0","method":"notifications/example","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}` + "\n")
	require.NoError(t, err)
	modernListMsg := sendAndRead(`{"jsonrpc":"2.0","id":6,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	assert.JSONEq(t, `6`, string(modernListMsg.ID))
	assert.JSONEq(t, `{"legacy":false}`, string(modernListMsg.Result))

	require.NoError(t, clientOut.Close())
	select {
	case serveErr := <-serveDone:
		require.NoError(t, serveErr)
	case <-time.After(2 * time.Second):
		t.Fatal("ServeStdio did not exit after client closed stdin")
	}
}

func TestStdioServerSubprocessConcurrencyCancellationAndProgress(t *testing.T) {
	caller, err := NewStdioCaller(t.Context(), StdioOptions{
		Command:      os.Args[0],
		Args:         []string{"-test.run=^TestStdioServerHelperProcess$"},
		Env:          []string{"GOA_AI_STDIO_SERVER_HELPER=1"},
		ClientInfo:   ClientInfo{Name: "stdio-peer", Version: "1"},
		InputSupport: InputSupport{Form: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		assert.NoError(t, caller.Close(ctx))
	})

	// 1. Start a slow request, wait for its progress notification proving it is
	// running on the server, and cancel its context.
	cancelCtx, cancelSlow := context.WithCancel(t.Context())
	defer cancelSlow()
	slowStarted := make(chan struct{})
	slowCtx := WithProgress(cancelCtx, func(_ context.Context, _ Progress) error {
		close(slowStarted)
		return nil
	})
	slowErrCh := make(chan error, 1)
	go func() {
		_, callErr := caller.CallTool(slowCtx, CallRequest{Tool: "slow-cancel", Payload: json.RawMessage(`{}`)})
		slowErrCh <- callErr
	}()
	select {
	case <-slowStarted:
	case err := <-slowErrCh:
		t.Fatalf("slow-cancel failed before starting: %v", err)
	}
	cancelSlow()
	require.ErrorIs(t, <-slowErrCh, context.Canceled)

	// 2. Concurrent requests complete out of order with exact ID correlation.
	gatedStarted := make(chan struct{})
	gatedCtx := WithProgress(t.Context(), func(_ context.Context, _ Progress) error {
		close(gatedStarted)
		return nil
	})
	gatedResCh := make(chan CallResponse, 1)
	gatedErrCh := make(chan error, 1)
	go func() {
		res, callErr := caller.CallTool(gatedCtx, CallRequest{Tool: "wait-gate", Payload: json.RawMessage(`{}`)})
		gatedResCh <- res
		gatedErrCh <- callErr
	}()
	select {
	case <-gatedStarted:
	case err := <-gatedErrCh:
		t.Fatalf("wait-gate failed before starting: %v", err)
	}

	fastRes, err := caller.CallTool(t.Context(), CallRequest{Tool: "fast", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.JSONEq(t, `"fast-done"`, string(fastRes.StructuredContent))

	require.NoError(t, <-gatedErrCh)
	gatedRes := <-gatedResCh
	assert.JSONEq(t, `"gated"`, string(gatedRes.StructuredContent))

	// 3. Progress reporting delivers notifications/progress and closes after return.
	var updates []Progress
	progCtx := WithProgress(t.Context(), func(_ context.Context, update Progress) error {
		updates = append(updates, update)
		return nil
	})
	progRes, err := caller.CallTool(progCtx, CallRequest{Tool: "progress", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.JSONEq(t, `"progressed"`, string(progRes.StructuredContent))
	require.Len(t, updates, 1)
	assert.InDelta(t, 50.0, updates[0].Value, 1e-9)
	require.NotNil(t, updates[0].Total)
	assert.InDelta(t, 100.0, *updates[0].Total, 1e-9)
	require.NotNil(t, updates[0].Message)
	assert.Equal(t, "halfway", *updates[0].Message)

	// 4. Subscriptions: completed, pre-ack rejection, and post-ack cancellation.
	var events []SubscriptionEvent
	err = caller.Listen(t.Context(), SubscriptionFilter{
		ToolsListChanged:      true,
		ResourceSubscriptions: []string{"record://doc"},
	}, func(_ context.Context, event SubscriptionEvent) error {
		events = append(events, event)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.Equal(t, SubscriptionAcknowledged, events[0].Kind)
	assert.Equal(t, SubscriptionToolsChanged, events[1].Kind)
	assert.Equal(t, SubscriptionResourceUpdated, events[2].Kind)
	assert.Equal(t, "record://doc/child", events[2].URI)

	err = caller.Listen(t.Context(), SubscriptionFilter{
		ResourceSubscriptions: []string{"record://deny"},
	}, func(context.Context, SubscriptionEvent) error {
		return nil
	})
	require.ErrorContains(t, err, "denied before ack")

	err = caller.Listen(t.Context(), SubscriptionFilter{
		ResourceSubscriptions: []string{"record://cancel-after-ack"},
	}, func(context.Context, SubscriptionEvent) error {
		return nil
	})
	var cancelled *SubscriptionCancelledError
	require.ErrorAs(t, err, &cancelled)
	require.NotNil(t, cancelled.Reason)
	assert.Equal(t, "source stopped", *cancelled.Reason)
}

func TestStdioServerHelperProcess(_ *testing.T) {
	mode := os.Getenv("GOA_AI_STDIO_SERVER_HELPER")
	if mode == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	if mode == "expect-context-canceled-after-delay" {
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()
	}

	gate := make(chan struct{})
	err := ServeStdio(ctx, os.Stdin, os.Stdout, func(reqCtx context.Context, req *jsonrpc.RawRequest, _ bool) *jsonrpc.Response {
		switch req.Method {
		case "tools/list":
			tools := make([]map[string]any, 0, 6)
			for _, name := range []string{"slow-cancel", "wait-gate", "fast", "progress", "self-cancel", "flood-stdout"} {
				tools = append(tools, map[string]any{
					"name":         name,
					"description":  name,
					"inputSchema":  map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
					"outputSchema": map[string]any{"type": "string"},
				})
			}
			return jsonrpc.MakeSuccessResponse(req.ID, map[string]any{
				"resultType": "complete",
				"tools":      tools,
				"ttlMs":      0,
				"cacheScope": "private",
			})
		case methodToolsCall:
			var params struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InvalidParams, err.Error(), nil)
			}
			switch params.Name {
			case "slow-cancel":
				if err := ReportProgress(reqCtx, 1, nil, nil); err != nil {
					return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InternalError, err.Error(), nil)
				}
				<-reqCtx.Done()
				return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InternalError, reqCtx.Err().Error(), nil)
			case "wait-gate":
				if err := ReportProgress(reqCtx, 1, nil, nil); err != nil {
					return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InternalError, err.Error(), nil)
				}
				<-gate
				return jsonrpc.MakeSuccessResponse(req.ID, map[string]any{
					"resultType":        "complete",
					"content":           []any{},
					"structuredContent": "gated",
				})
			case "fast":
				close(gate)
				return jsonrpc.MakeSuccessResponse(req.ID, map[string]any{
					"resultType":        "complete",
					"content":           []any{},
					"structuredContent": "fast-done",
				})
			case "progress":
				total := 100.0
				msg := "halfway"
				if err := ReportProgress(reqCtx, 50, &total, &msg); err != nil {
					return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InternalError, err.Error(), nil)
				}
				if err := ReportProgress(reqCtx, 50, &total, nil); err == nil {
					return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InternalError, "expected non-increasing progress error", nil)
				}
				return jsonrpc.MakeSuccessResponse(req.ID, map[string]any{
					"resultType":        "complete",
					"content":           []any{},
					"structuredContent": "progressed",
				})
			case "self-cancel":
				// Cancel the server's root context while the parent keeps stdin open.
				cancel()
				<-reqCtx.Done()
				return nil
			case "flood-stdout":
				// Cancel the server's root context shortly after flooding stdout so
				// a blocked write on the full stdout pipe must be interrupted.
				go func() {
					time.Sleep(80 * time.Millisecond)
					cancel()
				}()
				chunk := strings.Repeat("x", 32*1024)
				for step := 1.0; ; step++ {
					if err := ReportProgress(reqCtx, step, nil, &chunk); err != nil {
						return nil
					}
				}
			default:
				return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InvalidParams, "unknown tool", nil)
			}
		case methodSubscriptionsListen:
			return ServeStdioSubscriptions(reqCtx, req, func(subCtx context.Context) *jsonrpc.Response {
				var input struct {
					Notifications SubscriptionFilter `json:"notifications"`
				}
				if err := json.Unmarshal(req.Params, &input); err != nil {
					return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InvalidParams, err.Error(), nil)
				}
				if len(input.Notifications.ResourceSubscriptions) == 1 && input.Notifications.ResourceSubscriptions[0] == "record://deny" {
					return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InvalidParams, "denied before ack", nil)
				}
				if err := AcknowledgeSubscription(subCtx, input.Notifications); err != nil {
					return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InternalError, err.Error(), nil)
				}
				if len(input.Notifications.ResourceSubscriptions) == 1 && input.Notifications.ResourceSubscriptions[0] == "record://cancel-after-ack" {
					return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InternalError, "source stopped", nil)
				}
				if input.Notifications.ToolsListChanged {
					if err := ReportToolsChanged(subCtx); err != nil {
						return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InternalError, err.Error(), nil)
					}
				}
				for _, uri := range input.Notifications.ResourceSubscriptions {
					if err := ReportResourceUpdated(subCtx, uri+"/child"); err != nil {
						return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InternalError, err.Error(), nil)
					}
				}
				return jsonrpc.MakeSuccessResponse(req.ID, map[string]any{
					"resultType": "complete",
					"_meta":      map[string]any{"io.modelcontextprotocol/serverInfo": map[string]any{"name": "stdio-sub", "version": "1"}},
				})
			})
		default:
			return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.MethodNotFound, "Method not found", nil)
		}
	})
	cancel()
	if mode == "expect-context-canceled" || mode == "expect-context-canceled-after-delay" {
		if !errors.Is(err, context.Canceled) {
			if _, writeErr := fmt.Fprintf(os.Stderr, "expected context.Canceled, got %v\n", err); writeErr != nil {
				os.Exit(3)
			}
			os.Exit(2)
		}
		os.Exit(0)
	}
	if err != nil {
		if _, writeErr := fmt.Fprintln(os.Stderr, err); writeErr != nil {
			os.Exit(3)
		}
		os.Exit(2)
	}
	os.Exit(0)
}

func TestStdioServerCleanContextShutdown(t *testing.T) {
	t.Run("pipe context cancellation", func(t *testing.T) {
		serverIn, clientOut, err := os.Pipe()
		require.NoError(t, err)
		clientIn, serverOut, err := os.Pipe()
		require.NoError(t, err)
		t.Cleanup(func() {
			if closeErr := clientOut.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				t.Errorf("close clientOut: %v", closeErr)
			}
			if closeErr := clientIn.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				t.Errorf("close clientIn: %v", closeErr)
			}
		})

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			done <- ServeStdio(ctx, serverIn, serverOut, func(context.Context, *jsonrpc.RawRequest, bool) *jsonrpc.Response {
				return nil
			})
		}()

		cancel()
		select {
		case serveErr := <-done:
			require.ErrorIs(t, serveErr, context.Canceled)
		case <-time.After(2 * time.Second):
			t.Fatal("ServeStdio did not exit promptly after context cancellation")
		}
	})

	t.Run("EOF cancels active ctx-waiting work", func(t *testing.T) {
		serverIn, clientOut, err := os.Pipe()
		require.NoError(t, err)
		clientIn, serverOut, err := os.Pipe()
		require.NoError(t, err)
		t.Cleanup(func() {
			if closeErr := clientIn.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				t.Errorf("close clientIn: %v", closeErr)
			}
		})

		started := make(chan struct{})
		handlerDone := make(chan error, 1)
		done := make(chan error, 1)
		go func() {
			done <- ServeStdio(t.Context(), serverIn, serverOut, func(reqCtx context.Context, req *jsonrpc.RawRequest, _ bool) *jsonrpc.Response {
				return ServeStdioSubscriptions(reqCtx, req, func(subCtx context.Context) *jsonrpc.Response {
					if ackErr := AcknowledgeSubscription(subCtx, SubscriptionFilter{ToolsListChanged: true}); ackErr != nil {
						return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InternalError, ackErr.Error(), nil)
					}
					close(started)
					<-subCtx.Done()
					handlerDone <- subCtx.Err()
					return nil
				})
			})
		}()

		_, err = clientOut.WriteString(`{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":{"notifications":{"toolsListChanged":true},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}` + "\n")
		require.NoError(t, err)

		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("subscription handler did not start")
		}

		// Close client input without sending notifications/cancelled.
		require.NoError(t, clientOut.Close())

		select {
		case handlerErr := <-handlerDone:
			require.ErrorIs(t, handlerErr, context.Canceled)
		case <-time.After(2 * time.Second):
			t.Fatal("active subscription handler was not cancelled on EOF")
		}

		select {
		case serveErr := <-done:
			require.NoError(t, serveErr)
		case <-time.After(2 * time.Second):
			t.Fatal("ServeStdio did not finish after EOF")
		}
	})

	t.Run("subprocess context cancellation with open stdin", func(t *testing.T) {
		//nolint:gosec // Test launches its own test binary as a stdio server helper process.
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestStdioServerHelperProcess$")
		cmd.Env = append(os.Environ(), "GOA_AI_STDIO_SERVER_HELPER=expect-context-canceled")
		stdin, err := cmd.StdinPipe()
		require.NoError(t, err)
		t.Cleanup(func() {
			if closeErr := stdin.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				t.Errorf("close stdin: %v", closeErr)
			}
		})
		stdout, err := cmd.StdoutPipe()
		require.NoError(t, err)
		t.Cleanup(func() {
			if closeErr := stdout.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				t.Errorf("close stdout: %v", closeErr)
			}
		})
		require.NoError(t, cmd.Start())

		_, err = stdin.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"self-cancel","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}` + "\n"))
		require.NoError(t, err)

		waitDone := make(chan error, 1)
		go func() {
			waitDone <- cmd.Wait()
		}()
		select {
		case waitErr := <-waitDone:
			require.NoError(t, waitErr)
		case <-time.After(2 * time.Second):
			t.Fatal("subprocess ServeStdio hung with open stdin on context cancellation")
		}
	})

	t.Run("subprocess context cancellation with blocked stdout", func(t *testing.T) {
		//nolint:gosec // Test launches its own test binary as a stdio server helper process.
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestStdioServerHelperProcess$")
		cmd.Env = append(os.Environ(), "GOA_AI_STDIO_SERVER_HELPER=expect-context-canceled")
		stdin, err := cmd.StdinPipe()
		require.NoError(t, err)
		t.Cleanup(func() {
			if closeErr := stdin.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				t.Errorf("close stdin: %v", closeErr)
			}
		})
		stdout, err := cmd.StdoutPipe()
		require.NoError(t, err)
		t.Cleanup(func() {
			if closeErr := stdout.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				t.Errorf("close stdout: %v", closeErr)
			}
		})
		require.NoError(t, cmd.Start())

		// Never read from stdout so the OS pipe buffer fills and blocks writeLine.
		_, err = stdin.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"flood-stdout","_meta":{"progressToken":"flood-1","io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}` + "\n"))
		require.NoError(t, err)

		waitDone := make(chan error, 1)
		go func() {
			waitDone <- cmd.Wait()
		}()
		select {
		case waitErr := <-waitDone:
			require.NoError(t, waitErr)
		case <-time.After(2 * time.Second):
			t.Fatal("subprocess ServeStdio hung with blocked stdout on context cancellation")
		}
	})

	t.Run("pipe context cancellation with malformed input and full stdout", func(t *testing.T) {
		serverIn, clientOut, err := os.Pipe()
		require.NoError(t, err)
		clientIn, serverOut, err := os.Pipe()
		require.NoError(t, err)
		t.Cleanup(func() {
			if closeErr := clientOut.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				t.Errorf("close clientOut: %v", closeErr)
			}
			if closeErr := clientIn.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				t.Errorf("close clientIn: %v", closeErr)
			}
		})

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			done <- ServeStdio(ctx, serverIn, serverOut, func(context.Context, *jsonrpc.RawRequest, bool) *jsonrpc.Response {
				return nil
			})
		}()

		// Send an invalid request whose echoed ID exceeds the OS pipe capacity while
		// clientIn is unread so writing the protocol error blocks on serverOut.
		largeID := strings.Repeat("x", 256*1024)
		_, err = clientOut.WriteString(`{"jsonrpc":"2.0","id":"` + largeID + `","method":"tools/list","params":{}}` + "\n" + "{invalid json\n")
		require.NoError(t, err)

		time.Sleep(50 * time.Millisecond)
		cancel()

		select {
		case serveErr := <-done:
			require.ErrorIs(t, serveErr, context.Canceled)
		case <-time.After(2 * time.Second):
			t.Fatal("ServeStdio hung on context cancellation when protocol error write blocked on full stdout")
		}
	})

	t.Run("subprocess context cancellation with malformed input and blocked stdout", func(t *testing.T) {
		//nolint:gosec // Test launches its own test binary as a stdio server helper process.
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestStdioServerHelperProcess$")
		cmd.Env = append(os.Environ(), "GOA_AI_STDIO_SERVER_HELPER=expect-context-canceled-after-delay")
		stdin, err := cmd.StdinPipe()
		require.NoError(t, err)
		t.Cleanup(func() {
			if closeErr := stdin.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				t.Errorf("close stdin: %v", closeErr)
			}
		})
		stdout, err := cmd.StdoutPipe()
		require.NoError(t, err)
		t.Cleanup(func() {
			if closeErr := stdout.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				t.Errorf("close stdout: %v", closeErr)
			}
		})
		require.NoError(t, cmd.Start())

		largeID := strings.Repeat("x", 256*1024)
		_, err = stdin.Write([]byte(`{"jsonrpc":"2.0","id":"` + largeID + `","method":"tools/list","params":{}}` + "\n" + "{invalid json\n"))
		require.NoError(t, err)

		waitDone := make(chan error, 1)
		go func() {
			waitDone <- cmd.Wait()
		}()
		select {
		case waitErr := <-waitDone:
			require.NoError(t, waitErr)
		case <-time.After(2 * time.Second):
			t.Fatal("subprocess ServeStdio hung with malformed input and blocked stdout on context cancellation")
		}
	})

	t.Run("EOF with blocked response writer holding writeMu on full stdout", func(t *testing.T) {
		serverIn, clientOut, err := os.Pipe()
		require.NoError(t, err)
		clientIn, serverOut, err := os.Pipe()
		require.NoError(t, err)
		t.Cleanup(func() {
			if closeErr := clientIn.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				t.Errorf("close clientIn: %v", closeErr)
			}
		})

		firstWriteDone := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- ServeStdio(t.Context(), serverIn, serverOut, func(reqCtx context.Context, _ *jsonrpc.RawRequest, _ bool) *jsonrpc.Response {
				chunk := strings.Repeat("z", 32*1024)
				for step := 1.0; ; step++ {
					if err := ReportProgress(reqCtx, step, nil, &chunk); err != nil {
						return nil
					}
					if step == 1 {
						close(firstWriteDone)
					}
				}
			})
		}()

		_, err = clientOut.WriteString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"flood","_meta":{"progressToken":"p-1","io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}` + "\n")
		require.NoError(t, err)

		select {
		case <-firstWriteDone:
		case <-time.After(2 * time.Second):
			t.Fatal("progress writer did not start")
		}

		// Allow subsequent 32 KiB progress frames to fill the unread pipe buffer so
		// the worker goroutine is blocked in pollable.Write holding writeMu when EOF arrives.
		time.Sleep(50 * time.Millisecond)
		require.NoError(t, clientOut.Close())

		select {
		case serveErr := <-done:
			require.NoError(t, serveErr)
		case <-time.After(2 * time.Second):
			t.Fatal("ServeStdio deadlocked on EOF while response writer held writeMu on full stdout")
		}
	})
}
