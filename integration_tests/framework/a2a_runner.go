package framework

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
)

var (
	a2aCodegenMu   sync.Mutex
	a2aCodegenOnce sync.Once
	a2aCodegenErr  error

	a2aBuildOnce     sync.Once
	a2aBuildErr      error
	a2aServerBinPath string
	a2aServerBinMu   sync.Mutex
)

// A2ARunner runs A2A scenarios against the generated A2A agent server.
type A2ARunner struct {
	server  *exec.Cmd
	baseURL string
	client  *http.Client

	stdoutTail *ringBuffer
	stderrTail *ringBuffer
	exitCh     chan error

	externalServer bool
}

// NewA2ARunner creates a new A2A runner.
func NewA2ARunner() *A2ARunner {
	return &A2ARunner{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// SupportsA2AServer reports whether the A2A integration framework can reach a server.
func SupportsA2AServer() bool {
	if os.Getenv("A2A_TEST_SERVER_URL") != "" {
		return true
	}
	return findA2AExampleRoot() != ""
}

// Run executes the A2A scenarios.
func (r *A2ARunner) Run(t *testing.T, scenarios []Scenario) error {
	t.Helper()
	if len(scenarios) == 0 {
		t.Skip("no scenarios to run")
	}

	if err := r.startServer(t); err != nil {
		return err
	}
	t.Cleanup(r.stopServer)

	for _, sc := range scenarios {
		scenario := sc
		t.Run(scenario.Name, func(t *testing.T) {
			t.Parallel()
			r.runSteps(t, scenario.Steps, scenario.Defaults, scenario.Pre)
		})
	}
	return nil
}

// findA2AExampleRoot locates the A2A example directory.
func findA2AExampleRoot() string {
	wd, _ := os.Getwd()
	for up := 0; up < 8; up++ {
		root := wd
		for i := 0; i < up; i++ {
			root = filepath.Dir(root)
		}
		fx := filepath.Join(root, "integration_tests", "fixtures", "a2a_agent")
		if st, err := os.Stat(fx); err == nil && st.IsDir() {
			if _, err := os.Stat(filepath.Join(fx, "go.mod")); err == nil {
				return fx
			}
		}
	}
	return ""
}

// findA2AServerCmdDir finds the A2A server command directory.
func findA2AServerCmdDir(exampleRoot string) (string, error) {
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
		if _, err := os.Stat(filepath.Join(d, "main.go")); err == nil {
			candidates = append(candidates, d)
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no server cmd dirs found under %s", cmdRoot)
	}
	for _, d := range candidates {
		if _, err := os.Stat(filepath.Join(d, "http.go")); err == nil {
			return d, nil
		}
	}
	return candidates[0], nil
}

// regenerateA2AExample regenerates the A2A example code.
func regenerateA2AExample(t *testing.T, exampleRoot string) error {
	t.Helper()
	a2aCodegenMu.Lock()
	defer a2aCodegenMu.Unlock()

	_ = os.RemoveAll(filepath.Join(exampleRoot, "cmd"))
	_ = os.RemoveAll(filepath.Join(exampleRoot, "gen"))

	tidyCmd := exec.CommandContext(context.Background(), "go", "mod", "tidy")
	tidyCmd.Dir = exampleRoot
	if out, err := tidyCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go mod tidy failed: %w\n%s", err, string(out))
	}

	genCmd := exec.CommandContext(
		context.Background(),
		"go", "run", "-C", exampleRoot,
		"goa.design/goa/v3/cmd/goa", "gen", "example.com/a2a_agent/design",
	)
	if out, err := genCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("goa gen failed: %w\n%s", err, string(out))
	}

	exCmd := exec.CommandContext(
		context.Background(),
		"go", "run", "-C", exampleRoot,
		"goa.design/goa/v3/cmd/goa", "example", "example.com/a2a_agent/design",
	)
	if out, err := exCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("goa example failed: %w\n%s", err, string(out))
	}

	postTidy := exec.CommandContext(context.Background(), "go", "mod", "tidy")
	postTidy.Dir = exampleRoot
	if out, err := postTidy.CombinedOutput(); err != nil {
		return fmt.Errorf("post goa example tidy failed: %w\n%s", err, string(out))
	}

	return nil
}

// buildA2AServerBinary compiles the A2A server binary.
//
//nolint:dupl // intentionally similar to buildServerBinary in runner.go for test isolation
func buildA2AServerBinary(exampleRoot string) (string, error) {
	a2aServerBinMu.Lock()
	defer a2aServerBinMu.Unlock()

	a2aBuildOnce.Do(func() {
		cmdPath, err := findA2AServerCmdDir(exampleRoot)
		if err != nil {
			a2aBuildErr = err
			return
		}

		tmpFile, err := os.CreateTemp("", "a2a-test-server-*")
		if err != nil {
			a2aBuildErr = fmt.Errorf("create temp file for binary: %w", err)
			return
		}
		binPath := tmpFile.Name()
		_ = tmpFile.Close()

		//nolint:gosec // launching 'go build' for test server is expected
		buildCmd := exec.CommandContext(context.Background(), "go", "build", "-o", binPath, ".")
		buildCmd.Dir = cmdPath
		out, err := buildCmd.CombinedOutput()
		if err != nil {
			_ = os.Remove(binPath)
			a2aBuildErr = fmt.Errorf("go build failed in %s: %w\n%s", cmdPath, err, string(out))
			return
		}

		if _, err := os.Stat(binPath); err != nil {
			a2aBuildErr = fmt.Errorf("binary not found after build: %w", err)
			return
		}
		a2aServerBinPath = binPath
	})

	return a2aServerBinPath, a2aBuildErr
}

// startServer starts the A2A test server.
func (r *A2ARunner) startServer(t *testing.T) error {
	t.Helper()
	if external := os.Getenv("A2A_TEST_SERVER_URL"); external != "" {
		u, err := url.Parse(external)
		if err != nil {
			return fmt.Errorf("parse A2A_TEST_SERVER_URL: %w", err)
		}
		if u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("invalid A2A_TEST_SERVER_URL %q", external)
		}
		u.RawQuery = ""
		u.Fragment = ""
		base := strings.TrimRight(u.String(), "/")
		if base == "" {
			return fmt.Errorf("invalid A2A_TEST_SERVER_URL %q", external)
		}
		r.baseURL = base
		r.externalServer = true
		return nil
	}

	port, err := getA2AFreePort()
	if err != nil {
		return err
	}
	r.baseURL = "http://localhost:" + port

	exampleRoot := findA2AExampleRoot()
	if exampleRoot == "" {
		return fmt.Errorf("could not locate A2A example root")
	}

	if !strings.EqualFold(os.Getenv("A2A_TEST_SKIP_GENERATION"), "true") {
		a2aCodegenOnce.Do(func() { a2aCodegenErr = regenerateA2AExample(t, exampleRoot) })
		if a2aCodegenErr != nil {
			return a2aCodegenErr
		}
	}

	binPath, err := buildA2AServerBinary(exampleRoot)
	if err != nil {
		return err
	}

	//nolint:gosec // launching pre-compiled test server binary
	cmd := exec.CommandContext(context.Background(), binPath, "-http-port", port)
	cmd.Env = os.Environ()

	r.stdoutTail = &ringBuffer{max: tailMaxBytes}
	r.stderrTail = &ringBuffer{max: tailMaxBytes}
	cmd.Stdout = r.stdoutTail
	cmd.Stderr = r.stderrTail

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start A2A server: %w", err)
	}
	r.server = cmd

	r.exitCh = make(chan error, 1)
	go func() {
		r.exitCh <- cmd.Wait()
	}()

	timeout := 30 * time.Second
	if v := os.Getenv("A2A_TEST_READY_TIMEOUT_SECONDS"); v != "" {
		if sec, perr := strconv.Atoi(v); perr == nil && sec > 0 {
			timeout = time.Duration(sec) * time.Second
		}
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-r.exitCh:
			return fmt.Errorf(
				"A2A server exited early: %w\n-- stdout --\n%s\n-- stderr --\n%s",
				err, string(r.stdoutTail.Bytes()), string(r.stderrTail.Bytes()),
			)
		default:
		}
		if err := r.ping(); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	return fmt.Errorf(
		"A2A server failed to become ready at %s\n-- stdout --\n%s\n-- stderr --\n%s",
		r.baseURL, string(r.stdoutTail.Bytes()), string(r.stderrTail.Bytes()),
	)
}

// stopServer stops the A2A test server.
func (r *A2ARunner) stopServer() {
	if r.externalServer {
		return
	}
	if r.server == nil || r.server.Process == nil {
		return
	}

	_ = r.server.Process.Signal(syscall.SIGINT)
	if r.exitCh != nil {
		select {
		case <-r.exitCh:
			return
		case <-time.After(2 * time.Second):
		}
	}

	_ = r.server.Process.Signal(syscall.SIGTERM)
	if r.exitCh != nil {
		select {
		case <-r.exitCh:
			return
		case <-time.After(1 * time.Second):
		}
	}

	_ = r.server.Process.Kill()
	if r.exitCh != nil {
		select {
		case <-r.exitCh:
		case <-time.After(1 * time.Second):
		}
	}
}

// ping checks if the A2A server is ready.
func (r *A2ARunner) ping() error {
	b := []byte(`{"jsonrpc":"2.0","id":1,"method":"agent/card","params":{}}`)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, r.baseURL+"/a2a", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return nil
}

// runSteps executes A2A test steps.
func (r *A2ARunner) runSteps(t *testing.T, steps []Step, _ *Defaults, _ *Pre) {
	for _, st := range steps {
		method := a2aMethodFromOp(st.Op)

		accept := ""
		if st.Headers != nil {
			accept = strings.ToLower(st.Headers["Accept"])
		}
		isStream := accept == "text/event-stream" || st.StreamExpect != nil

		if isStream {
			r.runA2AStepStreaming(t, st, method)
			continue
		}

		r.runA2AStepNonStreaming(t, st, method)
	}
}

// a2aMethodFromOp maps operation names to A2A JSON-RPC method names.
func a2aMethodFromOp(op string) string {
	switch op {
	case "AgentCard":
		return "agent/card"
	case "TasksSend":
		return "tasks/send"
	case "TasksSendSubscribe":
		return "tasks/sendSubscribe"
	case "TasksGet":
		return "tasks/get"
	case "TasksCancel":
		return "tasks/cancel"
	case "TasksResubscribe":
		return "tasks/resubscribe"
	default:
		return op
	}
}

// runA2AStepNonStreaming executes a non-streaming A2A step.
func (r *A2ARunner) runA2AStepNonStreaming(t *testing.T, st Step, method string) {
	t.Helper()

	result, _, err := r.executeA2AJSONRPC(method, st.Input)

	if st.Expect != nil && st.Expect.Status == statusError {
		require.Error(t, err)
		if st.Expect.Error != nil && st.Expect.Error.Code != 0 {
			assert.Contains(t, err.Error(), strconv.Itoa(st.Expect.Error.Code))
		}
		if st.Expect.Error != nil && st.Expect.Error.Message != "" {
			assert.Contains(t, err.Error(), st.Expect.Error.Message)
		}
		return
	}

	require.NoError(t, err)
	if st.Expect != nil && st.Expect.Result != nil {
		validateSubset(t, result, st.Expect.Result)
	}
}

// runA2AStepStreaming executes a streaming A2A step.
func (r *A2ARunner) runA2AStepStreaming(t *testing.T, st Step, method string) {
	t.Helper()

	resEvents, err := r.executeA2ASSE(method, st.Input, st.StreamExpect)

	if st.Expect != nil && st.Expect.Status == statusError {
		require.Error(t, err)
		if st.Expect.Error != nil && st.Expect.Error.Code != 0 {
			assert.Contains(t, err.Error(), strconv.Itoa(st.Expect.Error.Code))
		}
		return
	}

	require.NoError(t, err)
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
}

// executeA2AJSONRPC sends an A2A JSON-RPC request.
func (r *A2ARunner) executeA2AJSONRPC(method string, input map[string]any) (map[string]any, []byte, error) {
	if input == nil {
		input = map[string]any{}
	}

	reqObj := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": input}
	body, _ := json.Marshal(reqObj)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, r.baseURL+"/a2a", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

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
		return nil, raw, fmt.Errorf("A2A error %d: %s", int(code), msg)
	}
	return env.Result, raw, nil
}

// executeA2ASSE sends an A2A request expecting SSE.
func (r *A2ARunner) executeA2ASSE(method string, input map[string]any, spec *StreamExpect) ([]sseEvent, error) {
	if input == nil {
		input = map[string]any{}
	}

	reqObj := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": input}
	body, _ := json.Marshal(reqObj)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, r.baseURL+"/a2a", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}

	return parseSSEEvents(resp.Body, spec)
}

// getA2AFreePort finds an available port.
func getA2AFreePort() (string, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
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

// parseSSEEvents parses SSE events from a reader.
func parseSSEEvents(r io.Reader, spec *StreamExpect) ([]sseEvent, error) {
	timeout := 10 * time.Second
	if spec != nil && spec.TimeoutMS > 0 {
		timeout = time.Duration(spec.TimeoutMS) * time.Millisecond
	}

	deadline := time.Now().Add(timeout)
	var events []sseEvent
	var cur sseEvent
	buf := make([]byte, 4096)

	for time.Now().Before(deadline) {
		n, err := r.Read(buf)
		if err != nil {
			if err == io.EOF {
				break
			}
			return events, err
		}

		lines := strings.Split(string(buf[:n]), "\n")
		for _, line := range lines {
			line = strings.TrimRight(line, "\r")
			if line == "" {
				if cur.Event != "" || len(cur.Data) > 0 {
					events = append(events, cur)
					cur = sseEvent{}
				}
				if spec != nil && spec.MinEvents > 0 && len(events) >= spec.MinEvents {
					return events, nil
				}
				continue
			}
			if after, ok := strings.CutPrefix(line, "event:"); ok {
				cur.Event = strings.TrimSpace(after)
				continue
			}
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				var m map[string]any
				_ = json.Unmarshal([]byte(after), &m)
				if cur.Data == nil {
					cur.Data = map[string]any{}
				}
				for k, v := range m {
					cur.Data[k] = v
				}
			}
		}
	}

	return events, nil
}
