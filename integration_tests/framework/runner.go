package framework

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"syscall"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Runner runs scenarios against the generated example server.
type Runner struct {
	server  *exec.Cmd
	baseURL string
	client  *http.Client

	stdoutTail *ringBuffer
	stderrTail *ringBuffer
	exitCh     chan error
}

// Scenario models a test scenario (new multi-step form only).
type Scenario struct {
	Name     string    `yaml:"name"`
	Defaults *Defaults `yaml:"defaults"`
	Pre      *Pre      `yaml:"pre"`
	Steps    []Step    `yaml:"steps"`
}

// Defaults apply to steps when not explicitly set in a step.
type Defaults struct {
	Client  string            `yaml:"client"`  // e.g., "jsonrpc.mcp_assistant" (hint to pick generated client)
	Headers map[string]string `yaml:"headers"` // default headers for all steps
}

// Pre controls scenario-level behavior (e.g., auto-initialize handshake).
type Pre struct {
	AutoInitialize *bool `yaml:"auto_initialize"` // default true
}

// Step defines a single operation invocation using a generated client.
type Step struct {
	Name         string            `yaml:"name"`
	Client       string            `yaml:"client"`       // overrides defaults.client
	Op           string            `yaml:"op"`           // generated endpoint method name, e.g., "ToolsCall"
	Input        map[string]any    `yaml:"input"`        // maps to payload fields
	Headers      map[string]string `yaml:"headers"`      // per-step headers (e.g., Accept)
	Notification bool              `yaml:"notification"` // send as JSON-RPC notification (no id)

	// Expectations
	Expect       *Expect       `yaml:"expect"`
	StreamExpect *StreamExpect `yaml:"stream_expect"`
}

// ExpectedError captures expected JSON-RPC error.
type ExpectedError struct {
	Code    int    `yaml:"code"`
	Message string `yaml:"message"`
}

// Expect describes non-streaming expectations.
type Expect struct {
	Status string         `yaml:"status"` // success | error | no_response
	Error  *ExpectedError `yaml:"error"`
	Result map[string]any `yaml:"result"`
}

// StreamExpect describes streaming expectations.
type StreamExpect struct {
	MinEvents int              `yaml:"min_events"`
	TimeoutMS int              `yaml:"timeout_ms"`
	Events    []StreamEventExp `yaml:"events"`
}

// StreamEventExp matches SSE event/data partially.
type StreamEventExp struct {
	Event string         `yaml:"event"`
	Data  map[string]any `yaml:"data"`
}

// scenariosFile is the YAML root.
type scenariosFile struct {
	Scenarios []Scenario `yaml:"scenarios"`
}

// ringBuffer captures only the last max bytes written.
type ringBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

// sseEvent represents a server-sent event.
type sseEvent struct {
	Event string
	Data  map[string]any
}

const tailMaxBytes = 4096

var (
	codegenMu   sync.Mutex
	codegenOnce sync.Once
	codegenErr  error
)

// LoadScenarios loads scenarios from a YAML file path.
func LoadScenarios(path string) ([]Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read scenarios: %w", err)
	}
	var f scenariosFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse scenarios: %w", err)
	}
	return f.Scenarios, nil
}

// NewRunner creates a new runner with fixed timeout.
func NewRunner() *Runner {
	return &Runner{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Run executes the scenarios (always parallel, no filtering).
func (r *Runner) Run(t *testing.T, scenarios []Scenario) error {
	t.Helper()
	if len(scenarios) == 0 {
		t.Skip("no scenarios to run")
	}

	for _, sc := range scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			t.Parallel()
			// Use a fresh runner instance per scenario to avoid shared mutable state
			lr := NewRunner()
			require.NoError(t, lr.startServer(t))
			defer lr.stopServer()
			lr.runSteps(t, sc.Steps, sc.Defaults, sc.Pre)
		})
	}
	return nil
}

// Write implements io.Writer keeping only the last max bytes.
func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.buf == nil {
		r.buf = make([]byte, 0, r.max)
	}
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
	return len(p), nil
}

// Bytes returns a copy of the buffer contents.
func (r *ringBuffer) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) == 0 {
		return nil
	}
	out := make([]byte, len(r.buf))
	copy(out, r.buf)
	return out
}

// validateSubset ensures expected fields are present in actual using testify assertions.
func validateSubset(t *testing.T, actual map[string]any, expected map[string]any) {
	for k, vexp := range expected {
		vact, ok := actual[k]
		require.Truef(t, ok, "missing key %q", k)
		switch ev := vexp.(type) {
		case map[string]any:
			am, ok := toMap(vact)
			require.Truef(t, ok, "key %q: expected object", k)
			validateSubset(t, am, ev)
		case []any:
			aarr, ok := vact.([]any)
			require.Truef(t, ok, "key %q: expected array", k)
			require.GreaterOrEqualf(t, len(aarr), len(ev), "key %q: expected at least %d items, got %d", k, len(ev), len(aarr))
			for i := range ev {
				if elemExp, ok := ev[i].(map[string]any); ok {
					elemAct, ok := toMap(aarr[i])
					require.Truef(t, ok, "key %q[%d]: expected object", k, i)
					validateSubset(t, elemAct, elemExp)
				}
			}
		default:
			assert.Equalf(t, fmt.Sprintf("%v", vexp), fmt.Sprintf("%v", vact), "key %q mismatch", k)
		}
	}
}

// toMap converts various map types to map[string]any.
func toMap(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	if m, ok := v.(map[string]interface{}); ok {
		res := make(map[string]any, len(m))
		for k, vv := range m {
			res[k] = vv
		}
		return res, true
	}
	return nil, false
}

// getFreePort finds an available port on localhost.
func getFreePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listen for free port: %w", err)
	}
	defer func() { _ = l.Close() }()
	_, portStr, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		return "", err
	}
	return portStr, nil
}

// methodFromOp maps operation names to JSON-RPC method names.
func methodFromOp(op string) string {
	switch op {
	case "Initialize":
		return "initialize"
	case "Ping":
		return "ping"
	case "ToolsList":
		return "tools/list"
	case "ToolsCall":
		return "tools/call"
	case "ResourcesList":
		return "resources/list"
	case "ResourcesRead":
		return "resources/read"
	case "ResourcesSubscribe":
		return "resources/subscribe"
	case "ResourcesUnsubscribe":
		return "resources/unsubscribe"
	case "PromptsList":
		return "prompts/list"
	case "PromptsGet":
		return "prompts/get"
	case "NotifyStatusUpdate":
		return "notify_status_update"
	case "Subscribe":
		return "subscribe"
	case "Unsubscribe":
		return "unsubscribe"
	default:
		return op
	}
}

// findExampleRoot locates the example directory.
func findExampleRoot() string {
	wd, _ := os.Getwd()
	for up := 0; up < 8; up++ {
		root := wd
		for i := 0; i < up; i++ {
			root = filepath.Dir(root)
		}
		ex := filepath.Join(root, "example")
		if st, err := os.Stat(ex); err == nil && st.IsDir() {
			// Prefer example with its own go.mod
			if _, err := os.Stat(filepath.Join(ex, "go.mod")); err == nil {
				return ex
			}
			return ex
		}
	}
	return ""
}

// findServerCmdDir finds the server command directory.
func findServerCmdDir(exampleRoot string) (string, error) {
	cmdRoot := filepath.Join(exampleRoot, "cmd")
	entries, err := os.ReadDir(cmdRoot)
	if err != nil {
		return "", fmt.Errorf("read cmd root: %w", err)
	}
	var candidates []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d := filepath.Join(cmdRoot, e.Name())
		// Consider dirs that contain a main.go
		if _, err := os.Stat(filepath.Join(d, "main.go")); err == nil {
			candidates = append(candidates, d)
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no server cmd dirs found under %s", cmdRoot)
	}
	// Prefer one that also has http.go
	for _, d := range candidates {
		if _, err := os.Stat(filepath.Join(d, "http.go")); err == nil {
			return d, nil
		}
	}
	// Fallback to first
	return candidates[0], nil
}

// regenerateExample regenerates the example code.
func regenerateExample(t *testing.T, exampleRoot string) error {
	t.Helper()
	codegenMu.Lock()
	defer codegenMu.Unlock()
	// Clean example mains and generated example tests
	_ = os.RemoveAll(filepath.Join(exampleRoot, "cmd"))
	_ = filepath.WalkDir(exampleRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			// Only delete tests that contain the goa generated header
			f, oErr := os.Open(path)
			if oErr != nil {
				return nil
			}
			defer func() { _ = f.Close() }()
			buf := make([]byte, 2048)
			n, _ := f.Read(buf)
			if bytes.Contains(buf[:n], []byte("Code generated by goa")) {
				_ = os.Remove(path)
			}
		}
		return nil
	})
	// Run goa gen
	genCmd := exec.CommandContext(context.Background(), "go", "run", "-C", exampleRoot, "goa.design/goa/v3/cmd/goa", "gen", "example.com/assistant/design")
	if out, err := genCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("goa gen failed: %w\n%s", err, string(out))
	}
	// Remove top-level example stubs so goa example regenerates them with updated signatures
	_ = os.Remove(filepath.Join(exampleRoot, "assistant.go"))
	_ = os.Remove(filepath.Join(exampleRoot, "mcp_assistant.go"))
	_ = os.Remove(filepath.Join(exampleRoot, "streaming.go"))
	_ = os.Remove(filepath.Join(exampleRoot, "websocket.go"))
	_ = os.Remove(filepath.Join(exampleRoot, "grpcstream.go"))
	// Run goa example (we rely on goa >= v3.22.2 for mixed JSON-RPC ServeHTTP).
	exCmd := exec.CommandContext(context.Background(), "go", "run", "-C", exampleRoot, "goa.design/goa/v3/cmd/goa", "example", "example.com/assistant/design")
	if out, err := exCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("goa example failed: %w\n%s", err, string(out))
	}
	// Do not patch generated code; we only validate example generation.
	return nil
}

// startServer starts the test server.
func (r *Runner) startServer(t *testing.T) error {
	t.Helper()
	port, err := getFreePort()
	if err != nil {
		return err
	}
	r.baseURL = "http://localhost:" + port
	exampleRoot := findExampleRoot()
	if exampleRoot == "" {
		return fmt.Errorf("could not locate example root")
	}
	// Regenerate example code once for the entire test process
	codegenOnce.Do(func() { codegenErr = regenerateExample(t, exampleRoot) })
	if codegenErr != nil {
		return codegenErr
	}
	// Locate server command directory
	cmdPath, err := findServerCmdDir(exampleRoot)
	if err != nil {
		return err
	}
	// Allocate a free gRPC port to avoid conflicts (example defaults to 8080)
	grpcPort, err := getFreePort()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(context.Background(), "go", "run", "-C", cmdPath, ".", "-http-port", port, "-grpc-port", grpcPort)
	// Capture bounded stdout/stderr tails for diagnostics
	r.stdoutTail = &ringBuffer{max: tailMaxBytes}
	r.stderrTail = &ringBuffer{max: tailMaxBytes}
	cmd.Stdout = r.stdoutTail
	cmd.Stderr = r.stderrTail
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}
	r.server = cmd
	// Supervise child: record exit as soon as it happens
	r.exitCh = make(chan error, 1)
	go func() {
		r.exitCh <- cmd.Wait()
	}()
	// Wait for readiness by polling /rpc with a benign request
	timeout := 30 * time.Second
	if v := os.Getenv("MCP_TEST_READY_TIMEOUT_SECONDS"); v != "" {
		if sec, perr := strconv.Atoi(v); perr == nil && sec > 0 {
			timeout = time.Duration(sec) * time.Second
		}
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-r.exitCh:
			return fmt.Errorf("server exited early: %w\n-- stdout (tail) --\n%s\n-- stderr (tail) --\n%s", err, string(r.stdoutTail.Bytes()), string(r.stderrTail.Bytes()))
		default:
		}
		if err := r.ping(); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Include last logs for diagnosis
	return fmt.Errorf("server failed to become ready at %s\n-- stdout (tail) --\n%s\n-- stderr (tail) --\n%s", r.baseURL, string(r.stdoutTail.Bytes()), string(r.stderrTail.Bytes()))
}

// stopServer stops the test server.
func (r *Runner) stopServer() {
	if r.server == nil || r.server.Process == nil {
		return
	}
	// Try graceful shutdown signals first
	_ = r.server.Process.Signal(syscall.SIGINT)
	if r.exitCh != nil {
		select {
		case <-r.exitCh:
			return
		case <-time.After(2 * time.Second):
		}
	} else {
		time.Sleep(200 * time.Millisecond)
	}
	_ = r.server.Process.Signal(syscall.SIGTERM)
	if r.exitCh != nil {
		select {
		case <-r.exitCh:
			return
		case <-time.After(1 * time.Second):
		}
	} else {
		time.Sleep(200 * time.Millisecond)
	}
	_ = r.server.Process.Kill()
	if r.exitCh != nil {
		select {
		case <-r.exitCh:
		case <-time.After(1 * time.Second):
		}
	}
}

// ping checks if the server is ready.
func (r *Runner) ping() error {
	// Send a minimal invalid JSON-RPC request that does not initialize state
	b := []byte(`{"jsonrpc":"2.0","id":1}`)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, r.baseURL+"/rpc", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return nil
}

// runSteps executes test steps.
func (r *Runner) runSteps(t *testing.T, steps []Step, defaults *Defaults, pre *Pre) {
	// Determine auto-init
	autoInit := false
	if pre != nil && pre.AutoInitialize != nil {
		autoInit = *pre.AutoInitialize
	}
	if autoInit {
		_ = r.ensureInitialized()
	}

	for _, st := range steps {
		// Merge headers
		headers := map[string]string{}
		if defaults != nil {
			for k, v := range defaults.Headers {
				headers[k] = v
			}
		}
		for k, v := range st.Headers {
			headers[k] = v
		}

		// Resolve method name from op
		method := methodFromOp(st.Op)

		// Decide transport by Accept header or presence of stream expectations
		accept := strings.ToLower(headers["Accept"])
		isStream := accept == "text/event-stream" || st.StreamExpect != nil

		if isStream {
			resEvents, err := r.executeSSE(method, st.Input, headers, st.StreamExpect)
			if st.Expect != nil && st.Expect.Status == "error" {
				require.Error(t, err)
				if st.Expect.Error != nil && st.Expect.Error.Code != 0 {
					assert.Contains(t, err.Error(), strconv.Itoa(st.Expect.Error.Code))
				}
				if st.Expect.Error != nil && st.Expect.Error.Message != "" {
					assert.Contains(t, err.Error(), st.Expect.Error.Message)
				}
				continue
			}
			require.NoError(t, err)
			// Validate stream
			if st.StreamExpect != nil {
				if st.StreamExpect.MinEvents > 0 {
					assert.GreaterOrEqual(t, len(resEvents), st.StreamExpect.MinEvents)
				}
				for i := range st.StreamExpect.Events {
					if i >= len(resEvents) {
						break
					}
					exp := st.StreamExpect.Events[i]
					act := resEvents[i]
					if exp.Event != "" {
						assert.Equal(t, exp.Event, act.Event)
					}
					if exp.Data != nil {
						validateSubset(t, act.Data, exp.Data)
					}
				}
			}
			continue
		}

		// Non-streaming
		notify := st.Notification || (st.Expect != nil && st.Expect.Status == "no_response")
		result, raw, err := r.executeJSONRPC(method, st.Input, headers, notify)
		if st.Expect != nil && st.Expect.Status == "no_response" {
			assert.Empty(t, raw)
			continue
		}
		if st.Expect != nil && st.Expect.Status == "error" {
			require.Error(t, err)
			if st.Expect.Error != nil && st.Expect.Error.Code != 0 {
				assert.Contains(t, err.Error(), strconv.Itoa(st.Expect.Error.Code))
			}
			if st.Expect.Error != nil && st.Expect.Error.Message != "" {
				assert.Contains(t, err.Error(), st.Expect.Error.Message)
			}
			continue
		}
		require.NoError(t, err)
		if st.Expect != nil && st.Expect.Result != nil {
			validateSubset(t, result, st.Expect.Result)
		}
	}
}

// ensureInitialized sends an initialize request.
func (r *Runner) ensureInitialized() error {
    payload := map[string]any{
        "protocolVersion": "2025-06-18",
        "capabilities":    map[string]any{"tools": true, "resources": true, "prompts": true},
        "clientInfo":      map[string]any{"name": "runner", "version": "1.0.0"},
    }
    _, _, err := r.executeJSONRPC("initialize", payload, map[string]string{"Content-Type": "application/json"}, true)
    return err
}

// executeJSONRPC sends a JSON-RPC request and returns the result map, raw bytes, and error.
func (r *Runner) executeJSONRPC(method string, input map[string]any, headers map[string]string, notification bool) (map[string]any, []byte, error) {
	if input == nil {
		input = map[string]any{}
	}
	// For our JSON-RPC, payload is under "params"
	params := input
	id := any(1)
	if notification {
		id = nil
	}
	reqObj := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if id != nil {
		reqObj["id"] = id
	}
	body, _ := json.Marshal(reqObj)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, r.baseURL+"/rpc", bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) == 0 {
		return nil, raw, nil
	}
	var env struct {
		Result map[string]any `json:"result"`
		Error  map[string]any `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, raw, fmt.Errorf("invalid response JSON: %w", err)
	}
	if env.Error != nil {
		code, _ := env.Error["code"].(float64)
		msg, _ := env.Error["message"].(string)
		return nil, raw, fmt.Errorf("MCP error %d: %s", int(code), msg)
	}
	return env.Result, raw, nil
}

// executeSSE sends a request expecting SSE and returns captured events.
func (r *Runner) executeSSE(method string, input map[string]any, headers map[string]string, spec *StreamExpect) ([]sseEvent, error) {
	if input == nil {
		input = map[string]any{}
	}
	reqObj := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": input}
	body, _ := json.Marshal(reqObj)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, r.baseURL+"/rpc", bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// Treat non-2xx as error, return body for diagnostics
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}

	timeout := 10 * time.Second
	if spec != nil && spec.TimeoutMS > 0 {
		timeout = time.Duration(spec.TimeoutMS) * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	reader := bufio.NewReader(resp.Body)
	var events []sseEvent
	var cur sseEvent
	sawErrorEvent := false
	var lastErrMsg string
	var lastErrCode any
	for {
		if time.Now().After(deadline) {
			break
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return events, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" { // event boundary
			if cur.Event != "" || len(cur.Data) > 0 {
				events = append(events, cur)
				if cur.Event == "error" {
					sawErrorEvent = true
					if eobj, ok := cur.Data["error"].(map[string]any); ok {
						lastErrCode = eobj["code"]
						if msg, ok2 := eobj["message"].(string); ok2 {
							lastErrMsg = msg
						}
					}
				}
				cur = sseEvent{}
			}
			if spec != nil && spec.MinEvents > 0 && len(events) >= spec.MinEvents {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			cur.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var m map[string]any
			_ = json.Unmarshal([]byte(data), &m)
			if cur.Data == nil {
				cur.Data = map[string]any{}
			}
			for k, v := range m {
				cur.Data[k] = v
			}
			continue
		}
	}
	if spec == nil && sawErrorEvent {
		return events, fmt.Errorf("MCP error %v: %s", lastErrCode, lastErrMsg)
	}
	return events, nil
}
