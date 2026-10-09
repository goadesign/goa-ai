// These tests call real generated HTTP endpoints to verify that the framework
// preserves exact URIs and typed resource contents. The synthetic service owns
// URI lookup and domain errors; the adapter owns protocol validation.
package assistantapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"

	genassistant "example.com/assistant/gen/assistant"
	genclient "example.com/assistant/gen/jsonrpc/mcp_assistant/client"
	genserver "example.com/assistant/gen/jsonrpc/mcp_assistant/server"
	genmcp "example.com/assistant/gen/mcp_assistant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mcpruntime "goa.design/goa-ai/runtime/mcp"
	goahttp "goa.design/goa/v3/http"
	goa "goa.design/goa/v3/pkg"
)

type (
	// resourceTestService records the generated URI input and returns typed output.
	resourceTestService struct {
		genassistant.Service
		calls             int
		address           string
		result            *genassistant.ReadResourceResult
		failure           error
		completionCalls   int
		completionPayload *genassistant.SuggestArgumentPayload
	}
)

func (s *resourceTestService) ReadResource(_ context.Context, p *genassistant.ReadResourcePayload) (*genassistant.ReadResourceResult, error) {
	s.calls++
	s.address = p.Address
	return s.result, s.failure
}

// SuggestArgument records partial input and prior variables without reading a resource.
func (s *resourceTestService) SuggestArgument(_ context.Context, p *genassistant.SuggestArgumentPayload) (*genassistant.SuggestArgumentResult, error) {
	s.completionCalls++
	s.completionPayload = p
	total := int64(250)
	return &genassistant.SuggestArgumentResult{Values: []string{p.Value + "-first", p.Value + "-second"}, Total: &total}, nil
}

func TestResourceTemplateHTTP(t *testing.T) {
	service := &resourceTestService{Service: NewAssistant()}
	mux := goahttp.NewMuxer()
	adapter := genmcp.NewMCPAdapter(genassistant.NewEndpoints(service), nil)
	server := genserver.New(genmcp.NewEndpoints(adapter), mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, nil)
	genserver.Mount(mux, server)
	endpoint := httptest.NewServer(mux)
	defer endpoint.Close()
	location, err := url.Parse(endpoint.URL)
	require.NoError(t, err)
	client := genclient.NewClient(location.Scheme, location.Host, endpoint.Client(), goahttp.RequestEncoder, goahttp.ResponseDecoder, false)

	listed, err := client.ResourcesTemplatesList()(t.Context(), &genmcp.ResourceTemplatesListPayload{})
	require.NoError(t, err)
	catalog := listed.(*genmcp.ResourceTemplatesListResult)
	assert.Len(t, catalog.ResourceTemplates, 4)
	assert.Equal(t, "test://template/{id:3}/data", catalog.ResourceTemplates[1].URITemplate)
	assert.Zero(t, service.calls)
	_, err = client.ResourcesTemplatesList()(t.Context(), &genmcp.ResourceTemplatesListPayload{Cursor: new("next")})
	assert.Error(t, err)

	completed, err := client.CompletionComplete()(t.Context(), &genmcp.CompletionCompletePayload{
		Ref:      &genmcp.CompletionReference{Type: "ref/resource", URI: new("test://reserved/{+path}{?fields*}")},
		Argument: &genmcp.CompletionArgument{Name: "path", Value: "a%2F"},
		Context:  &genmcp.CompletionContext{Arguments: map[string]string{"fields": "name,size"}},
	})
	require.NoError(t, err)
	suggestions := completed.(*genmcp.CompletionCompleteResult).Completion
	assert.Equal(t, []string{"a%2F-first", "a%2F-second"}, suggestions.Values)
	assert.Equal(t, new(int64(250)), suggestions.Total)
	assert.Equal(t, "a%2F", service.completionPayload.Value)
	assert.Equal(t, map[string]string{"fields": "name,size"}, service.completionPayload.Arguments)
	assert.Zero(t, service.calls)
	for _, request := range []*genmcp.CompletionCompletePayload{
		{Ref: &genmcp.CompletionReference{Type: "ref/resource", URI: new("test://missing/{id}")}, Argument: &genmcp.CompletionArgument{Name: "id", Value: "x"}},
		{Ref: &genmcp.CompletionReference{Type: "ref/resource", URI: new("test://template/{id}/data")}, Argument: &genmcp.CompletionArgument{Name: "missing", Value: "x"}},
		{Ref: &genmcp.CompletionReference{Type: "ref/resource", URI: new("test://template/{id}/data")}, Argument: &genmcp.CompletionArgument{Name: "id", Value: "x"}, Context: &genmcp.CompletionContext{Arguments: map[string]string{"missing": "x"}}},
	} {
		before := service.completionCalls
		_, err := client.CompletionComplete()(t.Context(), request)
		assert.Error(t, err)
		assert.Equal(t, before, service.completionCalls)
	}
	unbound, err := client.CompletionComplete()(t.Context(), &genmcp.CompletionCompletePayload{
		Ref: &genmcp.CompletionReference{Type: "ref/resource", URI: new("test://template/{id:3}/data")}, Argument: &genmcp.CompletionArgument{Name: "id", Value: "x"},
	})
	require.NoError(t, err)
	assert.Empty(t, unbound.(*genmcp.CompletionCompleteResult).Completion.Values)
	for _, uri := range []string{"test://template/abc/data", "test://template/abcdef/data", "test://reserved/a%2Fb/c?fields=x,y", "test://reserved/%E2%82%AC"} {
		text := &genassistant.TemplateItem{}
		text.Selected.SetText(&genassistant.TemplateText{URI: uri, Text: ""})
		blob := &genassistant.TemplateItem{}
		blob.Selected.SetBlob(&genassistant.TemplateBlob{URI: "test://binary/secondary", Blob: []byte{}})
		service.result = &genassistant.ReadResourceResult{Parts: []*genassistant.TemplateItem{text, blob}}
		before := service.calls
		got, err := client.ResourcesRead()(t.Context(), &genmcp.ResourcesReadPayload{URI: uri})
		require.NoError(t, err)
		result, ok := got.(*genmcp.ResourcesReadResult).Outcome.AsComplete()
		require.True(t, ok)
		assert.Equal(t, before+1, service.calls)
		assert.Equal(t, uri, service.address)
		assert.Len(t, result.Contents, 2)
		assert.Equal(t, uri, result.Contents[0].URI)
		assert.Equal(t, new(""), result.Contents[0].Text)
		assert.Equal(t, new(""), result.Contents[1].Blob)
	}
	before := service.calls
	_, err = client.ResourcesRead()(t.Context(), &genmcp.ResourcesReadPayload{URI: "doc://list"})
	assert.NoError(t, err)
	assert.Equal(t, before, service.calls)
	for _, uri := range []string{"relative", "test://bad%ZZ"} {
		before := service.calls
		_, err = client.ResourcesRead()(t.Context(), &genmcp.ResourcesReadPayload{URI: uri})
		assert.Error(t, err)
		assert.Equal(t, before, service.calls)
	}
	for _, result := range []*genassistant.ReadResourceResult{nil, {}, {Parts: []*genassistant.TemplateItem{nil}}, {Parts: []*genassistant.TemplateItem{{}}}} {
		service.result = result
		_, err = client.ResourcesRead()(t.Context(), &genmcp.ResourcesReadPayload{URI: "test://template/abc/data"})
		var failure *mcpruntime.Error
		require.True(t, errors.As(err, &failure), "expected protocol error, got %v", err)
		assert.Equal(t, -32603, failure.Code)
	}
	service.failure = goa.PermanentError("invalid_params", "resource does not exist")
	_, err = client.ResourcesRead()(t.Context(), &genmcp.ResourcesReadPayload{URI: "unknown://resource"})
	var failure *mcpruntime.Error
	require.True(t, errors.As(err, &failure), "expected protocol error, got %v", err)
	assert.Equal(t, -32602, failure.Code)
}
