// These checks exercise task replies before a generated HTTP client decodes
// them. Valid null retention survives, while omitted metadata and incomplete
// task states fail at the shared protocol transport.
package mcp

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPTransportTaskResponseContract(t *testing.T) {
	metadata := `"taskId":"job","status":"working","createdAt":"created","lastUpdatedAt":"updated"`
	for _, tc := range []struct {
		name, method, result, failure string
	}{
		{"creation unlimited", methodToolsCall, `{"resultType":"task",` + metadata + `,"ttlMs":null}`, ""},
		{"creation finite", methodToolsCall, `{"resultType":"task",` + metadata + `,"ttlMs":9007199254740993}`, ""},
		{"creation absent retention", methodToolsCall, `{"resultType":"task",` + metadata + `}`, "ttlMs is required"},
		{"creation fractional retention", methodToolsCall, `{"resultType":"task",` + metadata + `,"ttlMs":0.5}`, "ttlMs"},
		{"get unlimited", methodTasksGet, `{"resultType":"complete",` + metadata + `,"ttlMs":null}`, ""},
		{"get another task", methodTasksGet, `{"resultType":"complete","taskId":"other","status":"working","createdAt":"created","lastUpdatedAt":"updated","ttlMs":null}`, "another task ID"},
		{"get absent retention", methodTasksGet, `{"resultType":"complete",` + metadata + `}`, "ttlMs is required"},
		{"get incomplete result", methodTasksGet, `{"resultType":"complete","taskId":"job","status":"completed","createdAt":"created","lastUpdatedAt":"updated","ttlMs":null}`, "completed task result"},
		{"get unknown status", methodTasksGet, `{"resultType":"complete","taskId":"job","status":"unknown","createdAt":"created","lastUpdatedAt":"updated","ttlMs":null}`, "unsupported task status"},
		{"update acknowledgment", methodTasksUpdate, `{"resultType":"complete"}`, ""},
		{"cancel acknowledgment", methodTasksCancel, `{"resultType":"complete"}`, ""},
		{"update malformed metadata", methodTasksUpdate, `{"resultType":"complete","_meta":null}`, "_meta"},
		{"cancel malformed metadata", methodTasksCancel, `{"resultType":"complete","_meta":null}`, "_meta"},
		{"ordinary tool result", methodToolsCall, `{"resultType":"complete","content":[]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := NewHTTPTransport(transportFunc(func(request *http.Request) (*http.Response, error) {
				require.NoError(t, request.Body.Close())
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":"request","result":` + tc.result + `}`)),
				}, nil
			}), ClientInfo{}, HTTPBindings{}, InputSupport{}, HTTPRetryPolicy{})
			params := `{"taskId":"job","inputResponses":{}}`
			if tc.method == methodToolsCall {
				params = `{"name":"start","arguments":{}}`
			}
			request, err := http.NewRequestWithContext(WithTaskSupport(t.Context()), http.MethodPost, "https://example.test/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"request","method":"`+tc.method+`","params":`+params+`}`))
			require.NoError(t, err)
			response, err := transport.Do(request)
			if tc.failure != "" {
				require.ErrorContains(t, err, tc.failure)
				assert.Nil(t, response)
				return
			}
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			assert.NoError(t, response.Body.Close())
			assert.Contains(t, string(body), tc.result)
		})
	}
}
