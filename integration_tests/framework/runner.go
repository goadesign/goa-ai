package framework

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
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
	"gopkg.in/yaml.v3"
)

const (
	initializeMethod  = "initialize"
	initializedMethod = "notifications/initialized"
	statusError       = "error"
)

// Runner runs scenarios against the generated example server.
type Runner struct {
	server          *exec.Cmd
	baseURL         *url.URL
	client          *http.Client
	protocolVersion string

	stdoutTail *ringBuffer
	stderrTail *ringBuffer
	exitCh     chan error

	externalServer bool
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
	Headers map[string]string `yaml:"headers"`
}

// Pre controls scenario-level behavior (e.g., auto-initialize handshake).
type Pre struct {
	AutoInitialize *bool `yaml:"auto_initialize"` // default true
}

// Step defines a single operation invocation using a generated client.
type Step struct {
	Name    string            `yaml:"name"`
	Op      string            `yaml:"op"`
	Input   map[string]any    `yaml:"input"`
	Headers map[string]string `yaml:"headers"`
	Expect  *Expect           `yaml:"expect"`
}

// ExpectedError captures expected JSON-RPC error.
type ExpectedError struct {
	Code    int    `yaml:"code"`
	Message string `yaml:"message"`
}

// Expect describes non-streaming expectations.
type Expect struct {
	Status string         `yaml:"status"` // success | error
	Error  *ExpectedError `yaml:"error"`
	Result map[string]any `yaml:"result"`
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

const tailMaxBytes = 4096

var (
	codegenMu   sync.Mutex
	codegenOnce sync.Once
	codegenErr  error

	// Pre-compiled binary state: compile once, run many instances.
	buildOnce     sync.Once
	buildErr      error
	serverBinPath string
	serverBinMu   sync.Mutex

	// reservedPorts records every port handed to a server in this test
	// process. Free ports are discovered by binding ":0" and closing the
	// listener before the server binds, so without this reservation two
	// parallel runners can be handed the same ephemeral port and race to
	// bind it.
	reservedPortsMu sync.Mutex
	reservedPorts   = map[string]struct{}{}
)

// LoadScenarios loads scenarios from a YAML file path.
func LoadScenarios(path string) ([]Scenario, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- test helper reads scenarios file from testdata path
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

// SupportsServer reports whether the integration framework can reach a server.
func SupportsServer() bool {
	if os.Getenv("TEST_SERVER_URL") != "" {
		return true
	}
	return findExampleRoot() != ""
}

// Run executes the scenarios (always parallel, no filtering).
func (r *Runner) Run(t *testing.T, scenarios []Scenario) error {
	t.Helper()
	if len(scenarios) == 0 {
		t.Skip("no scenarios to run")
	}

	if err := r.startServer(t); err != nil {
		return err
	}
	// Use t.Cleanup instead of defer so stopServer runs after all parallel
	// subtests complete. With defer, stopServer would run immediately when
	// Run returns (before parallel subtests execute).
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
			require.Len(t, aarr, len(ev), "key %q: array length mismatch", k)
			for i := range ev {
				if elemExp, ok := ev[i].(map[string]any); ok {
					elemAct, ok := toMap(aarr[i])
					require.Truef(t, ok, "key %q[%d]: expected object", k, i)
					validateSubset(t, elemAct, elemExp)
					continue
				}
				assert.Equalf(t, fmt.Sprintf("%v", ev[i]), fmt.Sprintf("%v", aarr[i]), "key %q[%d] mismatch", k, i)
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

// getFreePort finds an available localhost port that no other runner in this
// test process has been handed. The kernel can re-allocate an ephemeral port
// as soon as the probing listener closes, so uniqueness across parallel
// runners must be enforced here rather than assumed.
func getFreePort() (string, error) {
	for range 16 {
		l, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx // test helper just picks a free port
		if err != nil {
			return "", fmt.Errorf("listen for free port: %w", err)
		}
		_, portStr, err := net.SplitHostPort(l.Addr().String())
		closeErr := l.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", fmt.Errorf("close free-port listener: %w", closeErr)
		}
		reservedPortsMu.Lock()
		_, taken := reservedPorts[portStr]
		if !taken {
			reservedPorts[portStr] = struct{}{}
		}
		reservedPortsMu.Unlock()
		if !taken {
			return portStr, nil
		}
	}
	return "", errors.New("could not reserve a unique free port")
}

// methodFromOp maps operation names to JSON-RPC method names.
func methodFromOp(op string) string {
	switch op {
	case "Initialize":
		return initializeMethod
	case "NotificationsInitialized":
		return initializedMethod
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
	case "PromptsList":
		return "prompts/list"
	case "PromptsGet":
		return "prompts/get"
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
		// Use integration test fixture module exclusively
		fx := filepath.Join(root, "integration_tests", "fixtures", "assistant")
		if st, err := os.Stat(fx); err == nil && st.IsDir() {
			if _, err := os.Stat(filepath.Join(fx, "go.mod")); err == nil {
				return fx
			}
		}
	}
	return ""
}

// findServerCmdDir returns the one server command owned by this fixture.
func findServerCmdDir(exampleRoot string) (string, error) {
	dir := filepath.Join(exampleRoot, "cmd", "orchestrator")
	for _, name := range []string{"main.go", "http.go"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return "", fmt.Errorf("fixture server command is missing %s: %w", name, err)
		}
	}
	return dir, nil
}

// regenerateExample regenerates the example code.
func regenerateExample(t *testing.T, exampleRoot string) error {
	t.Helper()
	codegenMu.Lock()
	defer codegenMu.Unlock()
	if err := cleanGeneratedExampleArtifacts(exampleRoot); err != nil {
		return err
	}
	// Ensure module dependencies are present
	tidyCmd := exec.CommandContext(
		context.Background(),
		"go",
		"mod",
		"tidy",
	)
	tidyCmd.Dir = exampleRoot
	if out, err := tidyCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go mod tidy failed: %w\n%s", err, string(out))
	}
	// Run goa gen
	genCmd := exec.CommandContext(
		context.Background(),
		"go",
		"run",
		"-C",
		exampleRoot,
		"goa.design/goa/v3/cmd/goa",
		"gen",
		"example.com/assistant/design",
	) // #nosec G204
	if out, err := genCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("goa gen failed: %w\n%s", err, string(out))
	}
	// Rebuild the generated MCP adapter stub. The authored assistant fixture
	// remains in place so its stable results continue to test the wire codecs.
	for _, name := range []string{"mcp_assistant.go"} {
		if err := os.Remove(filepath.Join(exampleRoot, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove generated example stub %s: %w", name, err)
		}
	}
	// Run goa example.
	exCmd := exec.CommandContext(
		context.Background(),
		"go",
		"run",
		"-C",
		exampleRoot,
		"goa.design/goa/v3/cmd/goa",
		"example",
		"example.com/assistant/design",
	) // #nosec G204
	if out, err := exCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("goa example failed: %w\n%s", err, string(out))
	}
	// Tidy again after generation to pull any new deps from example scaffolding.
	postTidy := exec.CommandContext(
		context.Background(),
		"go",
		"mod",
		"tidy",
	)
	postTidy.Dir = exampleRoot
	if out, err := postTidy.CombinedOutput(); err != nil {
		return fmt.Errorf("post goa example tidy failed: %w\n%s", err, string(out))
	}
	// Do not patch generated code; we only validate example generation.
	return nil
}

// cleanGeneratedExampleArtifacts removes disposable example outputs so each
// regeneration starts from the same baseline. The cleanup is intentionally
// strict: leaked generated mains or stale generated tests should fail the
// regeneration rather than silently pollute the fixture tree.
func cleanGeneratedExampleArtifacts(exampleRoot string) (err error) {
	root, err := os.OpenRoot(exampleRoot)
	if err != nil {
		return fmt.Errorf("open example root: %w", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close example root: %w", closeErr))
		}
	}()

	if err := root.RemoveAll("cmd"); err != nil {
		return fmt.Errorf("remove generated cmd tree: %w", err)
	}
	return fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, readErr := root.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read generated test candidate %s: %w", path, readErr)
		}
		if !bytes.Contains(content, []byte("Code generated by goa")) {
			return nil
		}
		if removeErr := root.Remove(path); removeErr != nil {
			return fmt.Errorf("remove generated test %s: %w", path, removeErr)
		}
		return nil
	})
}

// buildServerBinary compiles the server binary once for fast parallel test starts.
func buildServerBinary(exampleRoot string) (string, error) {
	serverBinMu.Lock()
	defer serverBinMu.Unlock()

	buildOnce.Do(func() {
		cmdPath, err := findServerCmdDir(exampleRoot)
		if err != nil {
			buildErr = err
			return
		}
		// Create a temp file for the binary
		tmpFile, err := os.CreateTemp("", "mcp-test-server-*")
		if err != nil {
			buildErr = fmt.Errorf("create temp file for binary: %w", err)
			return
		}
		binPath := filepath.Clean(tmpFile.Name())
		if err := tmpFile.Close(); err != nil {
			buildErr = fmt.Errorf("close temp file for binary: %w", err)
			return
		}

		// Build the server binary
		//nolint:gosec // launching 'go build' test server is expected
		buildCmd := exec.CommandContext(
			context.Background(),
			"go",
			"build",
			"-o",
			binPath,
			".",
		)
		buildCmd.Dir = cmdPath
		out, err := buildCmd.CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("go build failed in %s: %w\n%s", cmdPath, err, string(out))
			// #nosec G703 -- binPath is a temp file path from os.CreateTemp.
			if rerr := os.Remove(binPath); rerr != nil {
				buildErr = errors.Join(buildErr, fmt.Errorf("remove temp binary failed: %w", rerr))
			}
			return
		}
		// Verify binary exists
		// #nosec G703 -- binPath is a temp file path from os.CreateTemp.
		if _, err := os.Stat(binPath); err != nil {
			buildErr = fmt.Errorf("binary not found after build: %w", err)
			return
		}
		serverBinPath = binPath
	})

	return serverBinPath, buildErr
}

// startServer starts the test server.
func (r *Runner) startServer(t *testing.T) error {
	t.Helper()
	if external := os.Getenv("TEST_SERVER_URL"); external != "" {
		u, err := url.Parse(external)
		if err != nil {
			return fmt.Errorf("parse TEST_SERVER_URL: %w", err)
		}
		if u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("invalid TEST_SERVER_URL %q: must include scheme and host", external)
		}
		u.RawQuery = ""
		u.Fragment = ""
		u.Path = strings.TrimRight(u.Path, "/")
		r.baseURL = u
		r.externalServer = true
		return nil
	}
	exampleRoot := findExampleRoot()
	if exampleRoot == "" {
		return fmt.Errorf("could not locate example root")
	}
	// Regenerate example code once for the entire test process
	if !strings.EqualFold(os.Getenv("TEST_SKIP_GENERATION"), "true") {
		codegenOnce.Do(func() { codegenErr = regenerateExample(t, exampleRoot) })
		if codegenErr != nil {
			return codegenErr
		}
	}
	// Build server binary once, then start instances from the compiled binary
	binPath, err := buildServerBinary(exampleRoot)
	if err != nil {
		return err
	}
	// Port reservation cannot exclude processes outside this test binary, so a
	// server can still lose its port between the probing listener close and
	// its own bind. That race has exactly one signature: the generated main
	// shuts down cleanly logging "address already in use". Retry it with a
	// fresh port and fail fast on every other startup failure.
	const maxBindRaceRetries = 3
	for attempt := 0; ; attempt++ {
		err := r.launchServer(binPath)
		if err == nil {
			return nil
		}
		if attempt >= maxBindRaceRetries || !strings.Contains(err.Error(), "address already in use") {
			return err
		}
		t.Logf("retrying server start after port bind race (attempt %d): %v", attempt+1, err)
	}
}

// launchServer starts one server instance on a freshly reserved port and waits
// for readiness by polling /rpc. Failures embed the captured stdout/stderr
// tails so callers can distinguish the port bind race from real crashes.
func (r *Runner) launchServer(binPath string) error {
	port, err := getFreePort()
	if err != nil {
		return err
	}
	r.baseURL, err = url.Parse("http://localhost:" + port)
	if err != nil {
		return fmt.Errorf("parse local server URL: %w", err)
	}
	// Start HTTP server from pre-compiled binary (much faster than go run)
	//nolint:gosec // launching pre-compiled test server binary
	cmd := exec.CommandContext(context.Background(), binPath, "-http-port", port)
	// Inherit environment and propagate MCP_* variables captured for this scenario
	cmd.Env = os.Environ()
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
		case waitErr := <-r.exitCh:
			return fmt.Errorf(
				"server exited early (%s)\n-- stdout (tail) --\n%s\n-- stderr (tail) --\n%s",
				exitStatus(waitErr),
				string(r.stdoutTail.Bytes()),
				string(r.stderrTail.Bytes()),
			)
		default:
		}
		if err := r.ping(); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Include last logs for diagnosis
	return fmt.Errorf(
		"server failed to become ready at %s\n-- stdout (tail) --\n%s\n-- stderr (tail) --\n%s",
		r.baseURL,
		string(r.stdoutTail.Bytes()),
		string(r.stderrTail.Bytes()),
	)
}

// exitStatus renders a cmd.Wait result for diagnostics. The generated example
// main exits 0 even when its listener fails, so a nil error still means the
// server quit before becoming ready.
func exitStatus(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

// stopServer stops the test server.
func (r *Runner) stopServer() {
	if r.externalServer {
		return
	}
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
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, r.baseURL.String()+"/rpc", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	// #nosec G704 -- test runner issues requests to localhost (or a validated TEST_SERVER_URL)
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return nil
}

// runSteps executes test steps.
func (r *Runner) runSteps(t *testing.T, steps []Step, defaults *Defaults, pre *Pre) {
	t.Helper()
	autoInit := false
	if pre != nil && pre.AutoInitialize != nil {
		autoInit = *pre.AutoInitialize
	}
	if autoInit {
		require.NoError(t, r.ensureInitialized())
	}

	for _, st := range steps {
		// Merge headers
		headers := map[string]string{}
		if defaults != nil {
			maps.Copy(headers, defaults.Headers)
		}
		maps.Copy(headers, st.Headers)

		method := methodFromOp(st.Op)
		r.runStep(t, st, headers, method)
	}
}

// runStep sends one JSON-RPC message over HTTP and validates its response.
func (r *Runner) runStep(t *testing.T, st Step, headers map[string]string, method string) {
	t.Helper()
	notification := method == initializedMethod
	result, err := r.executeJSONRPC(method, st.Input, headers, notification)
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
	if method == initializeMethod {
		version, ok := result["protocolVersion"].(string)
		require.True(t, ok, "initialize response omitted protocolVersion")
		r.protocolVersion = version
	}
	if st.Expect != nil && st.Expect.Result != nil {
		validateSubset(t, result, st.Expect.Result)
	}
}

// ensureInitialized sends the request and notification that establish one MCP
// session. The negotiated version is attached to the notification and every
// later request.
func (r *Runner) ensureInitialized() error {
	payload := map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "runner", "version": "1.0.0"},
	}
	result, err := r.executeJSONRPC(initializeMethod, payload, nil, false)
	if err != nil {
		return err
	}
	version, ok := result["protocolVersion"].(string)
	if !ok {
		return errors.New("initialize response omitted protocolVersion")
	}
	r.protocolVersion = version
	_, err = r.executeJSONRPC(initializedMethod, nil, nil, true)
	return err
}

// executeJSONRPC sends one JSON-RPC message and returns its decoded result.
func (r *Runner) executeJSONRPC(
	method string,
	input map[string]any,
	headers map[string]string,
	notification bool,
) (map[string]any, error) {
	reqObj := map[string]any{"jsonrpc": "2.0", "method": method}
	if input != nil {
		reqObj["params"] = input
	}
	if !notification {
		reqObj["id"] = 1
	}
	body, err := json.Marshal(reqObj)
	if err != nil {
		return nil, fmt.Errorf("encode MCP request: %w", err)
	}
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		r.baseURL.String()+"/rpc",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("build MCP request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	if r.protocolVersion != "" && method != initializeMethod {
		req.Header.Set("MCP-Protocol-Version", r.protocolVersion)
	}
	// #nosec G704 -- test runner issues requests to localhost (or a validated TEST_SERVER_URL)
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read MCP response: %w", err)
	}
	if notification {
		if resp.StatusCode != http.StatusAccepted {
			return nil, fmt.Errorf("MCP notification returned HTTP %d: %s", resp.StatusCode, string(raw))
		}
		if len(raw) != 0 {
			return nil, errors.New("MCP notification returned a response body")
		}
		return nil, nil
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("MCP request returned HTTP %d without a response", resp.StatusCode)
	}
	var env struct {
		Result map[string]any `json:"result"`
		Error  map[string]any `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("invalid response JSON: %w", err)
	}
	if env.Error != nil {
		code, _ := env.Error["code"].(float64)
		msg, _ := env.Error["message"].(string)
		return nil, fmt.Errorf("MCP error %d: %s", int(code), msg)
	}
	return env.Result, nil
}
