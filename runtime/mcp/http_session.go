// This file implements the HTTP rules shared by handwritten and generated MCP
// clients. It adds the current session headers, turns a JSON or event-stream
// request response into one JSON body, answers server requests, and clears an
// expired session without repeating the failed operation.

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
)

// HTTPSession applies the Streamable HTTP rules for one MCP client session.
// Generated clients still create and validate their typed initialize messages.
type HTTPSession struct {
	next interface {
		Do(*http.Request) (*http.Response, error)
	}
	protocolVersion string
	mu              sync.RWMutex
	active          bool
	sessionID       string
}

// NewHTTPSession wraps next with the Streamable HTTP rules for protocolVersion.
func NewHTTPSession(next interface {
	Do(*http.Request) (*http.Response, error)
}, protocolVersion string) *HTTPSession {
	return &HTTPSession{
		next:            next,
		protocolVersion: protocolVersion,
	}
}

// Reset clears the current protocol and session headers before a generated or
// handwritten client sends a new initialize request.
func (s *HTTPSession) Reset() {
	s.mu.Lock()
	s.active = false
	s.sessionID = ""
	s.mu.Unlock()
}

// Begin adds the configured protocol version and any session identifier from
// the initialize response to the initialized notification and later requests.
func (s *HTTPSession) Begin() {
	s.mu.Lock()
	s.active = true
	s.mu.Unlock()
}

// Do sends one MCP message. Successful request responses are returned as one
// JSON body; notifications and HTTP failures keep their original response.
func (s *HTTPSession) Do(req *http.Request) (*http.Response, error) {
	message, err := readOutgoingMessage(req)
	if err != nil {
		return nil, err
	}
	sentSessionID := s.applyHeaders(req)
	resp, err := s.next.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound && sentSessionID != "" {
		s.expire(sentSessionID)
		return resp, nil
	}
	if resp.StatusCode == http.StatusOK && message.Method == rpcMethodInitialize {
		sessionID := resp.Header.Get("Mcp-Session-Id")
		if !validSessionID(sessionID) {
			return nil, closeResponseWithError(
				resp,
				NewMalformedResponseError(errors.New("MCP session ID must contain only visible ASCII characters")),
			)
		}
		s.mu.Lock()
		s.sessionID = sessionID
		s.mu.Unlock()
	}
	if resp.StatusCode != http.StatusOK || message.Method == "" || len(message.ID) == 0 {
		return resp, nil
	}
	return s.normalizeResponse(req.Context(), req, resp, message.ID)
}

// Initialized reports whether the session completed initialization after its
// most recent reset or expired-session response.
func (s *HTTPSession) Initialized() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active
}

// validSessionID accepts an omitted ID or bytes that are safe in an HTTP
// header, as required by MCP.
func validSessionID(sessionID string) bool {
	for i := range len(sessionID) {
		if sessionID[i] < 0x21 || sessionID[i] > 0x7e {
			return false
		}
	}
	return true
}

// applyHeaders replaces stale MCP headers with the values for the session that
// is active when the request is sent and returns the sent session identifier.
func (s *HTTPSession) applyHeaders(req *http.Request) string {
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Del("MCP-Protocol-Version")
	req.Header.Del("Mcp-Session-Id")
	s.mu.RLock()
	active := s.active
	sessionID := s.sessionID
	s.mu.RUnlock()
	if active {
		req.Header.Set("MCP-Protocol-Version", s.protocolVersion)
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
	}
	return req.Header.Get("Mcp-Session-Id")
}

// expire clears only the session that produced the 404 response. A concurrent
// initialize response may already have installed a different session.
func (s *HTTPSession) expire(expiredSessionID string) {
	s.mu.Lock()
	if s.sessionID == expiredSessionID {
		s.active = false
		s.sessionID = ""
	}
	s.mu.Unlock()
}

// normalizeResponse reads the server-selected response format and replaces it
// with the matching JSON response expected by the generated Goa decoder.
func (s *HTTPSession) normalizeResponse(
	ctx context.Context,
	original *http.Request,
	resp *http.Response,
	requestID json.RawMessage,
) (*http.Response, error) {
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return nil, closeResponseWithError(resp, NewMalformedResponseError(err))
	}
	var response []byte
	switch mediaType {
	case "application/json":
		response, err = io.ReadAll(resp.Body)
		if err == nil {
			err = validateResponseMessage(response, requestID)
		}
	case "text/event-stream":
		response, err = s.readEventStreamResponse(ctx, original, resp.Body, requestID)
	default:
		err = NewMalformedResponseError(fmt.Errorf("unsupported content type %q", mediaType))
	}
	closeErr := resp.Body.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(response))
	resp.ContentLength = int64(len(response))
	resp.Header.Set("Content-Type", "application/json")
	return resp, nil
}

// readEventStreamResponse reads events until the response identifier matches
// the client request. It answers server requests and ignores notifications.
func (s *HTTPSession) readEventStreamResponse(
	ctx context.Context,
	original *http.Request,
	body io.Reader,
	requestID json.RawMessage,
) ([]byte, error) {
	reader := bufio.NewReader(body)
	var data strings.Builder
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, NewMalformedResponseError(readErr)
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			response, found, err := s.handleEvent(ctx, original, data.String(), requestID)
			if err != nil {
				return nil, err
			}
			if found {
				return response, nil
			}
			data.Reset()
		} else if value, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(value, " "))
		}
		if errors.Is(readErr, io.EOF) {
			if data.Len() > 0 {
				response, found, err := s.handleEvent(ctx, original, data.String(), requestID)
				if err != nil {
					return nil, err
				}
				if found {
					return response, nil
				}
			}
			return nil, NewMalformedResponseError(errors.New("event stream ended before the request response"))
		}
	}
}

// handleEvent returns a matching response, answers a server request, or ignores
// a notification and lets the event reader continue.
func (s *HTTPSession) handleEvent(
	ctx context.Context,
	original *http.Request,
	data string,
	requestID json.RawMessage,
) ([]byte, bool, error) {
	if data == "" {
		return nil, false, nil
	}
	messageBytes := []byte(data)
	var message rpcMessage
	if err := json.Unmarshal(messageBytes, &message); err != nil {
		return nil, false, NewMalformedResponseError(err)
	}
	if message.JSONRPC != rpcVersion {
		return nil, false, NewMalformedResponseError(errors.New("JSON-RPC version must be 2.0"))
	}
	hasResult := message.Result != nil
	hasError := message.Error != nil
	if message.Method == "" {
		if len(message.ID) == 0 || hasResult == hasError {
			return nil, false, NewMalformedResponseError(errors.New("invalid JSON-RPC response"))
		}
		if bytes.Equal(bytes.TrimSpace(message.ID), bytes.TrimSpace(requestID)) {
			return append([]byte(nil), messageBytes...), true, nil
		}
		return nil, false, nil
	}
	if hasResult || hasError {
		return nil, false, NewMalformedResponseError(errors.New("JSON-RPC method message contains a response"))
	}
	if len(message.ID) == 0 {
		return nil, false, nil
	}
	if err := s.replyToServerRequest(ctx, original, message); err != nil {
		return nil, false, err
	}
	return nil, false, nil
}

// replyToServerRequest posts an empty ping result or a method-not-found error
// with the exact identifier sent by the server.
func (s *HTTPSession) replyToServerRequest(
	ctx context.Context,
	original *http.Request,
	message rpcMessage,
) (err error) {
	reply := rpcReply{JSONRPC: rpcVersion, ID: message.ID}
	if message.Method == "ping" {
		reply.Result = json.RawMessage(`{}`)
	} else {
		reply.Error = &rpcError{Code: JSONRPCMethodNotFound, Message: "method not found"}
	}
	body, err := json.Marshal(reply)
	if err != nil {
		return NewInternalError(err)
	}
	req := original.Clone(ctx)
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header = original.Header.Clone()
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, resp.Body.Close())
	}()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("MCP server request response returned HTTP status %d", resp.StatusCode)
	}
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return NewMalformedResponseError(err)
	}
	if len(responseBody) != 0 {
		return errors.New("MCP server request response must be empty")
	}
	return nil
}

// readOutgoingMessage preserves the request body while extracting the raw
// method and identifier needed to match a JSON or event-stream response.
func readOutgoingMessage(req *http.Request) (rpcMessage, error) {
	if req.Body == nil {
		return rpcMessage{}, nil
	}
	body, readErr := io.ReadAll(req.Body)
	closeErr := req.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return rpcMessage{}, NewInternalError(err)
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	var message rpcMessage
	if err := json.Unmarshal(body, &message); err != nil {
		return rpcMessage{}, NewInternalError(err)
	}
	return message, nil
}

// validateResponseMessage checks that one JSON object is the response to the
// outgoing request and sets exactly one of result or error.
func validateResponseMessage(data []byte, requestID json.RawMessage) error {
	var message rpcMessage
	if err := json.Unmarshal(data, &message); err != nil {
		return NewMalformedResponseError(err)
	}
	if err := message.validateResponse(); err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace(message.ID), bytes.TrimSpace(requestID)) {
		return NewMalformedResponseError(errors.New("invalid JSON-RPC response"))
	}
	return nil
}

// closeResponseWithError closes a response that cannot be returned to the
// caller and preserves both the validation and close failures.
func closeResponseWithError(resp *http.Response, err error) error {
	return errors.Join(err, resp.Body.Close())
}
