// These tests exercise silent HTTP responses and terminal write cleanup without
// asking service methods to manufacture progress or changing their result.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	// idleResponseReader applies the intermediary idle window to each network read.
	idleResponseReader struct {
		net.Conn
	}

	// deadlineRecordingWriter retains the deadline installed by its caller and
	// records any replacement attempted by the response transport.
	deadlineRecordingWriter struct {
		*httptest.ResponseRecorder
		deadline     time.Time
		replacements int
	}

	// blockedCommentWriter models a peer that stops accepting bytes until the
	// request's terminal deadline releases its pending write.
	blockedCommentWriter struct {
		header      http.Header
		entered     chan struct{}
		released    chan struct{}
		once        sync.Once
		deadlines   int
		deadlineErr error
	}
)

// Read renews the idle deadline before receiving more response bytes.
func (r idleResponseReader) Read(data []byte) (int, error) {
	if err := r.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		return 0, err
	}
	return r.Conn.Read(data)
}

func (w *blockedCommentWriter) Header() http.Header { return w.header }
func (w *blockedCommentWriter) WriteHeader(int)     {}
func (w *blockedCommentWriter) Write([]byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.released
	return 0, io.ErrClosedPipe
}
func (w *blockedCommentWriter) SetWriteDeadline(time.Time) error {
	w.deadlines++
	close(w.released)
	return w.deadlineErr
}

func TestSilentResponseSurvivesIdleIntermediary(t *testing.T) {
	// The TCP intermediary closes the response when it receives no bytes for
	// an idle window. The service remains silent for several such windows.
	result := `{"jsonrpc":"2.0","id":"silent","result":{"value":42}}`
	finished := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finished <- serveProgress(w, r, json.RawMessage(`{}`), func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(350 * time.Millisecond):
				_, err := io.WriteString(w, result)
				assert.NoError(t, err)
			case <-r.Context().Done():
			}
		}, 10*time.Millisecond)
	}))
	defer server.Close()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxyDone := make(chan error, 1)
	go func() {
		downstream, err := listener.Accept()
		if err != nil {
			proxyDone <- err
			return
		}
		upstream, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", strings.TrimPrefix(server.URL, "http://"))
		if err != nil {
			proxyDone <- errors.Join(err, downstream.Close())
			return
		}
		request, err := http.ReadRequest(bufio.NewReader(downstream))
		if err == nil {
			err = request.Write(upstream)
		}
		if err == nil {
			err = request.Body.Close()
		}
		if err == nil {
			// Copy network bytes as they arrive, but stop at the complete HTTP body.
			// A later socket close is not part of this response's delivery.
			reader := bufio.NewReader(io.TeeReader(idleResponseReader{upstream}, downstream))
			var response *http.Response
			response, err = http.ReadResponse(reader, request)
			if err == nil {
				_, copyErr := io.Copy(io.Discard, response.Body)
				err = errors.Join(copyErr, response.Body.Close())
			}
		}
		proxyDone <- errors.Join(err, upstream.Close(), downstream.Close())
	}()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+listener.Addr().String(), strings.NewReader(`{}`))
	require.NoError(t, err)
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Close = true
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, "text/event-stream", response.Header.Get("Content-Type"))
	assert.Greater(t, strings.Count(string(body), ":\n\n"), 3)
	assert.Equal(t, 1, strings.Count(string(body), "data: "+result))
	assert.NotContains(t, string(body), "notifications/progress")
	assert.NoError(t, <-finished)
	assert.NoError(t, <-proxyDone)
	assert.NoError(t, listener.Close())
}

func TestKeepalivePreservesOrdinaryResponses(t *testing.T) {
	for _, tc := range []struct {
		name, accept, method string
		slow                 bool
	}{
		{name: "fast", accept: "application/json, text/event-stream"},
		{name: "json", accept: "application/json", slow: true},
		{name: "excluded", accept: "*/*, text/event-stream;q=0", slow: true},
		{name: "task", accept: "application/json, text/event-stream", method: methodTasksGet, slow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
			request.Header.Set("Accept", tc.accept)
			request.Header.Set("Mcp-Method", tc.method)
			writer := httptest.NewRecorder()
			interval := time.Millisecond
			if !tc.slow {
				interval = time.Hour
			}
			err := serveProgress(writer, request, json.RawMessage(`{}`), func(w http.ResponseWriter, r *http.Request) {
				if tc.slow {
					time.Sleep(5 * time.Millisecond)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, err := io.WriteString(w, `{"jsonrpc":"2.0","id":7,"error":{"code":-32602,"message":"Invalid params"}}`)
				assert.NoError(t, err)
			}, interval)
			require.NoError(t, err)
			assert.Equal(t, http.StatusBadRequest, writer.Code)
			assert.Equal(t, "application/json", writer.Header().Get("Content-Type"))
			assert.NotContains(t, writer.Body.String(), "data:")
		})
	}
}

func TestKeepaliveJoinsBlockedWriteOnTermination(t *testing.T) {
	for _, panicHandler := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "panic"}[panicHandler], func(t *testing.T) {
			writer := &blockedCommentWriter{header: make(http.Header), entered: make(chan struct{}), released: make(chan struct{})}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/mcp", nil)
			request.Header.Set("Accept", "text/event-stream")
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() {
					if panicHandler {
						assert.Equal(t, "handler panic", recover())
					}
				}()
				err := serveProgress(writer, request, json.RawMessage(`{}`), func(w http.ResponseWriter, r *http.Request) {
					<-writer.entered
					if panicHandler {
						panic("handler panic")
					}
					cancel()
					<-r.Context().Done()
				}, time.Millisecond)
				if !panicHandler {
					assert.ErrorIs(t, err, io.ErrClosedPipe)
				}
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("response worker was not joined")
			}
			assert.Equal(t, 1, writer.deadlines)
		})
	}
}

func TestKeepaliveWriteFailureCancelsWaitingHandler(t *testing.T) {
	writer := &progressFailureWriter{header: make(http.Header), failure: io.ErrClosedPipe}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
	request.Header.Set("Accept", "text/event-stream")
	err := serveProgress(writer, request, json.RawMessage(`{}`), func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		assert.ErrorIs(t, context.Cause(r.Context()), io.ErrClosedPipe)
	}, time.Millisecond)
	require.ErrorIs(t, err, io.ErrClosedPipe)
	assert.Equal(t, 1, writer.writes)
}

func TestKeepaliveFramesFinalErrorWithoutProgressToken(t *testing.T) {
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
	request.Header.Set("Accept", "text/event-stream")
	writer := httptest.NewRecorder()
	result := `{"jsonrpc":"2.0","id":"original","error":{"code":-32603,"message":"service failure"}}`
	err := serveProgress(writer, request, json.RawMessage(`{}`), func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, ReportProgress(r.Context(), 1, nil, nil))
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
		_, err := io.WriteString(w, result)
		assert.NoError(t, err)
	}, time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, writer.Code)
	assert.Contains(t, writer.Body.String(), ":\n\n")
	assert.Equal(t, 1, strings.Count(writer.Body.String(), "data: "+result))
	assert.NotContains(t, writer.Body.String(), "notifications/progress")
}

func TestKeepalivePreservesDeadlineFailure(t *testing.T) {
	deadlineErr := errors.New("deadline failed")
	writer := &blockedCommentWriter{header: make(http.Header), entered: make(chan struct{}), released: make(chan struct{}), deadlineErr: deadlineErr}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/mcp", nil)
	request.Header.Set("Accept", "text/event-stream")
	err := serveProgress(writer, request, json.RawMessage(`{}`), func(w http.ResponseWriter, r *http.Request) {
		<-writer.entered
		cancel()
		<-r.Context().Done()
	}, time.Millisecond)
	require.ErrorIs(t, err, io.ErrClosedPipe)
	assert.ErrorIs(t, err, deadlineErr)
}

// TestProgressSenderOwnsTerminalError preserves completed/failed response errors
// even after request cleanup cancels the context retained by the service.
func TestProgressSenderOwnsTerminalError(t *testing.T) {
	failure := errors.New("original write failure")
	for _, tc := range []struct {
		name               string
		closed             bool
		failure, errorWant error
	}{
		{name: "active", errorWant: context.Canceled},
		{name: "failed", failure: failure, errorWant: failure},
		{name: "closed", closed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := &progressHTTPResponse{closed: tc.closed, failure: tc.failure}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			ctx = context.WithValue(ctx, progressSenderKey{}, progressSender(response.send))
			err := ReportProgress(ctx, 1, nil, nil)
			if tc.closed {
				assert.ErrorContains(t, err, "finished")
			} else {
				assert.ErrorIs(t, err, tc.errorWant)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorIs(t, ReportProgress(ctx, 1, nil, nil), context.Canceled)
}

func (w *deadlineRecordingWriter) SetWriteDeadline(deadline time.Time) error { //nolint:unparam // ResponseController requires this error return.
	w.deadline = deadline
	w.replacements++
	return nil
}

func TestKeepalivePreservesHealthyWriteDeadline(t *testing.T) {
	original := time.Now().Add(time.Hour)
	writer := &deadlineRecordingWriter{ResponseRecorder: httptest.NewRecorder(), deadline: original}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
	request.Header.Set("Accept", "text/event-stream")
	err := serveProgress(writer, request, json.RawMessage(`{}`), func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		_, err := io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
		assert.NoError(t, err)
	}, time.Millisecond)
	require.NoError(t, err)
	assert.Contains(t, writer.Body.String(), ":\n\n")
	assert.Equal(t, original, writer.deadline)
	assert.Zero(t, writer.replacements)
}

// A buffered fast response must commit headers at the same point as net/http.
func TestFastResponseCommitsHeaders(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(strconv.FormatBool(explicit), func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
			request.Header.Set("Accept", "application/json, text/event-stream")
			writer := httptest.NewRecorder()
			writer.Header().Set("X-Removed", "old")
			err := serveProgress(writer, request, json.RawMessage(`{}`), func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Del("X-Removed")
				if explicit {
					w.WriteHeader(http.StatusBadRequest)
				}
				_, err := io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid params"}}`)
				assert.NoError(t, err)
				w.Header().Set("Content-Type", "application/json+json")
			}, time.Hour)
			require.NoError(t, err)
			assert.Equal(t, "application/json", writer.Result().Header.Get("Content-Type"))
			assert.Empty(t, writer.Result().Header.Get("X-Removed"))
		})
	}
}
