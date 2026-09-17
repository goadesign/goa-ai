// These tests run the example through the official SDK against a local server.
// They verify generated loading choices and runtime replay without credentials.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"goa.design/goa-ai/features/model/openai"
)

func TestGeneratedAgentSearchesExecutesAndReplays(t *testing.T) {
	var mu sync.Mutex
	var requests []map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, request)
		index := len(requests)
		mu.Unlock()
		var output string
		switch index {
		case 1:
			output = `{"type":"tool_search_call","id":"search-1","call_id":"search-call-1","execution":"client","status":"completed","arguments":{"query":"answer question"}}`
		case 2:
			output = `{"type":"function_call","id":"function-1","call_id":"helper-1","name":"helpers_answer","status":"completed","arguments":"{\"question\":\"What is the capital of Japan?\"}"}`
		case 3:
			output = `{"type":"message","id":"message-1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Tokyo is the capital of Japan.","annotations":[]}]}`
		default:
			http.Error(w, "unexpected model request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		response := fmt.Sprintf(`{"status":"completed","model":"test-search-model","output":[%s],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`, output)
		if _, err := io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":"+response+"}\n\ndata: [DONE]\n\n"); err != nil {
			t.Errorf("write local model response: %v", err)
		}
	}))
	defer server.Close()
	sdk := openaisdk.NewClient(
		option.WithAPIKey("local-test"),
		option.WithBaseURL(server.URL),
		option.WithMaxRetries(0),
	)
	client, err := openai.New(openai.Options{Client: &sdk.Responses, DefaultModel: "test-search-model"})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(t.Context(), client, "test-search-model", &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "Tokyo is the capital of Japan.\n" {
		t.Fatalf("unexpected answer %q", output.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("expected search, selection, and result requests; got %d", len(requests))
	}
	initialTools := string(requests[0]["tools"])
	if !strings.Contains(initialTools, `"type":"tool_search"`) || strings.Contains(initialTools, "helpers_answer") {
		t.Fatalf("initial request did not hide the deferred helper: %s", initialTools)
	}
	searchInput := string(requests[1]["input"])
	if !strings.Contains(searchInput, `"type":"tool_search_output"`) || !strings.Contains(searchInput, `"name":"helpers_answer"`) {
		t.Fatalf("search did not return the helper contract: %s", searchInput)
	}
	history := string(requests[2]["input"])
	for _, expected := range []string{`"type":"tool_search_call"`, `"type":"tool_search_output"`, `"type":"function_call_output"`, "Tokyo"} {
		if !strings.Contains(history, expected) {
			t.Errorf("resume history is missing %s: %s", expected, history)
		}
	}
}

func TestGeneratedAgentReturnsProviderFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, err := io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"model_not_found","message":"The requested model does not exist."}}`)
		if err != nil {
			t.Errorf("write local model failure: %v", err)
		}
	}))
	defer server.Close()
	sdk := openaisdk.NewClient(
		option.WithAPIKey("local-test"),
		option.WithBaseURL(server.URL),
		option.WithMaxRetries(0),
	)
	client, err := openai.New(openai.Options{Client: &sdk.Responses, DefaultModel: "missing-model"})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = run(t.Context(), client, "missing-model", &output)
	if err == nil || !strings.Contains(err.Error(), "model_not_found") {
		t.Fatalf("expected the model failure, got %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("provider failure produced an answer: %s", output.String())
	}
}
