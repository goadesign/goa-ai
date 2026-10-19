// These tests compile generated paging contracts and exercise the exact input
// codecs used by static and registry callers. Provider branches retain their
// own typed payloads while only the query name remains public.
package tests

import (
	"testing"

	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

func TestGeneratedPagingComposition(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, pagingCompositionDesign)
	root := writeCompleteGeneratedModule(t, files)
	writeGeneratedPackageTest(t, root, "gen/consumer/toolsets/records/paging_test.go", `package records

import (
	"context"
	"encoding/json"
	"testing"

	genprovider "generated.local/gen/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/goa-ai/runtime/toolregistry/contract"
)

type pagingService struct {
	calls    []string
	sessions []string
}

func (s *pagingService) Read(_ context.Context, p *genprovider.Query) (*genprovider.Page, error) {
	s.sessions = append(s.sessions, p.SessionID)
	if p.Cursor == nil {
		s.calls = append(s.calls, "query")
	} else {
		s.calls = append(s.calls, "page")
	}
	return &genprovider.Page{Values: []string{"record"}, Returned: 1}, nil
}
func (s *pagingService) Start(context.Context, *genprovider.First) (*genprovider.Page, error) {
	s.calls = append(s.calls, "start")
	return &genprovider.Page{Returned: 1}, nil
}
func (s *pagingService) Next(context.Context, *genprovider.Next) (*genprovider.Page, error) {
	s.calls = append(s.calls, "next")
	return &genprovider.Page{Returned: 1}, nil
}
func TestProviderPaging(t *testing.T) {
	service := &pagingService{}
	provider := NewProvider(service)
	for _, test := range []struct {
		name     tools.Ident
		input    string
		textOnly bool
	}{
		{Same, "{\"selection\":{\"kind\":\"a\"}}", false},
		{Same, "{\"selection\":{\"kind\":\"a\"},\"cursor\":\"next\"}", true},
		{Different, "{\"selection\":{\"kind\":\"a\"}}", false},
		{Different, "{\"cursor\":\"next\"}", true},
	} {
		result, err := provider.HandleToolCall(toolregistry.WithToolUseID(context.Background(), "call"), toolregistry.ToolCallMessage{Tool: test.name, ToolUseID: "call", Payload: []byte(test.input), Meta: &toolregistry.ToolCallMeta{TextOnly: test.textOnly, SessionID: "session"}})
		require.NoError(t, err)
		require.Nil(t, result.Error)
	}
	assert.Equal(t, []string{"query", "page", "start", "next"}, service.calls)
	assert.Equal(t, []string{"session", "session"}, service.sessions)
	result, err := provider.HandleToolCall(toolregistry.WithToolUseID(context.Background(), "call"), toolregistry.ToolCallMessage{Tool: DifferentNext, ToolUseID: "call", Payload: []byte("{\"cursor\":\"next\"}"), Meta: &toolregistry.ToolCallMeta{}})
	require.NoError(t, err)
	require.NotNil(t, result.Error)
	assert.Len(t, service.calls, 4)
}
func TestPublishedPaging(t *testing.T) {
	require.Len(t, Specs(), 3)
	require.Len(t, ToolSchemas(), 3)
	require.Len(t, Names(), 3)
	require.Len(t, Metadata(), 3)
	for _, name := range []tools.Ident{SameNext, DifferentNext, EmptyNext} {
		_, ok := Spec(name)
		assert.False(t, ok)
	}
	for _, test := range []struct {
		name             tools.Ident
		query, page, bad string
	}{
		{Same, "{\"selection\":{\"kind\":\"a\"}}", "{\"selection\":{\"kind\":\"a\"},\"cursor\":\"next\"}", "{\"cursor\":\"next\"}"},
		{Different, "{\"selection\":{\"kind\":\"a\"}}", "{\"cursor\":\"next\"}", "{\"selection\":{\"kind\":\"a\"},\"cursor\":\"next\"}"},
		{Empty, "{}", "{\"cursor\":\"next\"}", "{\"cursor\":null}"},
	} {
		t.Run(string(test.name), func(t *testing.T) {
			spec, ok := Spec(test.name)
			require.True(t, ok)
			assert.Equal(t, test.name, spec.Bounds.Paging.ContinueTool)
			assert.Empty(t, spec.Bounds.Paging.SourceTool)
			_, err := spec.Payload.Codec.FromJSON([]byte(test.query))
			require.NoError(t, err)
			_, err = spec.Payload.Codec.FromJSON([]byte(test.page))
			assert.Error(t, err)
			for _, input := range []string{test.query, test.page} {
				value, err := spec.ExecutionPayloadCodec.FromJSON([]byte(input))
				require.NoError(t, err)
				encoded, err := spec.ExecutionPayloadCodec.ToJSON(value)
				require.NoError(t, err)
				assert.JSONEq(t, input, string(encoded))
				textValue, err := spec.TextOnly.ExecutionCodec.FromJSON([]byte(input))
				require.NoError(t, err)
				textJSON, err := spec.TextOnly.ExecutionCodec.ToJSON(textValue)
				require.NoError(t, err)
				if test.name == Same {
					var fields map[string]any
					require.NoError(t, json.Unmarshal(textJSON, &fields))
					assert.Equal(t, false, fields["render"])
				}
			}
			_, err = spec.ExecutionPayloadCodec.FromJSON([]byte(test.bad))
			assert.Error(t, err)
			for _, decl := range ToolSchemas() {
				if decl.Name != string(test.name) {
					continue
				}
				compiled, err := contract.Compile(decl)
				require.NoError(t, err)
				for _, input := range []string{test.query, test.page} {
					_, err = compiled.ExecutionPayloadCodec.FromJSON([]byte(input))
					require.NoError(t, err)
					textValue, err := compiled.TextOnly.ExecutionCodec.FromJSON([]byte(input))
					require.NoError(t, err)
					if test.name == Same {
						assert.Equal(t, false, textValue.(map[string]any)["render"])
					}
				}
			}
		})
	}
	for _, input := range []string{"{\"selection\":{\"kind\":\"a\"},\"render\":true}", "{\"selection\":{\"kind\":\"a\"},\"cursor\":\"next\",\"render\":true}"} {
		_, err := SpecSame().TextOnly.ExecutionCodec.FromJSON([]byte(input))
		assert.Error(t, err)
	}
	same, err := SpecSame().ExecutionPayloadCodec.FromJSON([]byte("{\"selection\":{\"kind\":\"a\"}}"))
	require.NoError(t, err)
	assert.IsType(t, &SamePayload{}, same)
	same, err = SpecSame().ExecutionPayloadCodec.FromJSON([]byte("{\"selection\":{\"kind\":\"a\"},\"cursor\":\"next\"}"))
	require.NoError(t, err)
	assert.IsType(t, &SameNextPayload{}, same)
	value, err := SpecDifferent().ExecutionPayloadCodec.FromJSON([]byte("{\"cursor\":\"next\"}"))
	require.NoError(t, err)
	assert.IsType(t, &DifferentNextPayload{}, value)
	value, err = SpecDifferent().ExecutionPayloadCodec.FromJSON([]byte("{\"selection\":{\"kind\":\"a\"}}"))
	require.NoError(t, err)
	assert.IsType(t, &DifferentPayload{}, value)
}
`)
	writeGeneratedPackageTest(t, root, "gen/native/agents/reader/agenttools/operations/paging_test.go", `package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNativePublishesSourceOnly(t *testing.T) {
	specs := agentToolSpecs()
	assert.Len(t, specs, 1)
	for _, spec := range specs {
		assert.NotContains(t, string(spec.Name), "_next")
	}
}
`)

	writeGeneratedPackageTest(t, root, "gen/consumer/agents/reader/records/executor_test.go", `package records

import (
	"context"
	"testing"

	genprovider "generated.local/gen/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestComposedServiceExecutor(t *testing.T) {
	var calls []string
	caller := func(name string) func(context.Context, any) (any, error) {
		return func(_ context.Context, input any) (any, error) {
			calls = append(calls, name)
			if query, ok := input.(*genprovider.Query); ok {
				assert.Equal(t, "session", query.SessionID)
			}
			return &genprovider.Page{Returned: 1}, nil
		}
	}
	executor := NewReaderRecordsExec(WithSame(caller("query")), WithSameNext(caller("page")), WithDifferent(caller("start")), WithDifferentNext(caller("next")))
	for _, test := range []struct{ name, input string }{
		{"records.same", "{\"selection\":{\"kind\":\"a\"}}"},
		{"records.same", "{\"selection\":{\"kind\":\"a\"},\"cursor\":\"next\"}"},
		{"records.different", "{\"selection\":{\"kind\":\"a\"}}"},
		{"records.different", "{\"cursor\":\"next\"}"},
	} {
		result, err := executor.Execute(context.Background(), &runtime.ToolCallMeta{SessionID: "session"}, &runtime.ToolCall{Name: tools.Ident(test.name), Payload: []byte(test.input)})
		require.NoError(t, err)
		require.NotNil(t, result.ToolResult)
		assert.Nil(t, result.ToolResult.Failure)
	}
	assert.Equal(t, []string{"query", "page", "start", "next"}, calls)
	result, err := executor.Execute(context.Background(), &runtime.ToolCallMeta{}, &runtime.ToolCall{Name: "records.same_next", Payload: []byte("{\"selection\":{\"kind\":\"a\"},\"cursor\":\"next\"}")})
	require.NoError(t, err)
	assert.NotNil(t, result.ToolResult.Failure)
	assert.Len(t, calls, 4)
}
`)
	runGeneratedPackageTest(t, root, "./gen/...")
}

func pagingCompositionDesign() {
	pagingCompositionCursorDesign(false, false)
}

func pagingCompositionCursorDesign(required, defaulted bool) {
	API("paging", func() {})
	selection := Type("Selection", func() {
		Attribute("kind", String, "Selected record kind.")
		Required("kind")
	})
	query := Type("Query", func() {
		Attribute("selection", selection, "Record selection.")
		Attribute("cursor", String, "Next record position.", func() {
			if defaulted {
				Default("initial")
			}
		})
		if required {
			Required("cursor")
		}
		Attribute("session_id", String, "Trusted session.")
		Attribute("render", Boolean, "Whether to display the records.")
		Required("selection", "session_id")
	})
	first := Type("First", func() {
		Attribute("selection", selection, "Record selection.")
		Required("selection")
	})
	next := Type("Next", func() {
		Attribute("cursor", String, "Next record position.")
		Required("cursor")
	})
	result := Type("Values", func() {
		Attribute("values", ArrayOf(String), "Returned records.")
	})
	page := Type("Page", func() {
		Attribute("values", ArrayOf(String), "Returned records.")
		Attribute("returned", Int, "Returned count.")
		Attribute("truncated", Boolean, "Whether more records remain.")
		Attribute("next_cursor", String, "Next record position.")
		Attribute("refinement_hint", String, "How to narrow the query.")
		Required("returned", "truncated")
	})
	shared := Toolset("records", func() {
		Tool("same", "Read selected records.", func() {
			Args(query)
			Inject("session_id")
			UIOnly("render")
			Return(result)
			BindTo("provider", "read")
			BoundedResult(func() {
				ContinueWith("same_next", "cursor")
				NextCursor("next_cursor")
			})
		})
		Tool("same_next", "Read the next records.", func() {
			Args(query)
			Inject("session_id")
			UIOnly("render")
			Return(result)
			BindTo("provider", "read")
			BoundedResult(func() {
				Cursor("cursor")
				NextCursor("next_cursor")
			})
		})
		Tool("different", "Start a record query.", func() {
			Args(first)
			Return(result)
			BindTo("provider", "start")
			BoundedResult(func() {
				ContinueWith("different_next", "cursor")
				NextCursor("next_cursor")
			})
		})
		Tool("different_next", "Read its next records.", func() {
			Args(next)
			Return(result)
			BindTo("provider", "next")
			BoundedResult(func() {
				Cursor("cursor")
				NextCursor("next_cursor")
			})
		})
		Tool("empty", "Read all records.", func() {
			Args(func() {})
			Return(result)
			BoundedResult(func() {
				ContinueWith("empty_next", "cursor")
				NextCursor("next_cursor")
			})
		})
		Tool("empty_next", "Read remaining records.", func() {
			Args(next)
			Return(result)
			BoundedResult(func() {
				Cursor("cursor")
				NextCursor("next_cursor")
			})
		})
	})
	Service("provider", func() {
		Method("read", func() {
			Payload(query)
			Result(page)
		})
		Method("start", func() {
			Payload(first)
			Result(page)
		})
		Method("next", func() {
			Payload(next)
			Result(page)
		})
	})
	Service("consumer", func() {
		Agent("reader", "Read records.", func() {
			Use(shared, func() {
				Deferred()
			})
		})
	})
	operations := Toolset("operations", func() {
		Tool("search", "Search records.", func() {
			Args(first)
			Return(result)
			BoundedResult(func() {
				ContinueWith("search_next", "cursor")
				NextCursor("next_cursor")
			})
		})
		Tool("search_next", "Read remaining records.", func() {
			Args(next)
			Return(result)
			BoundedResult(func() {
				Cursor("cursor")
				NextCursor("next_cursor")
			})
		})
	})
	Service("native", func() {
		Agent("reader", "Read records.", func() {
			Export(operations)
		})
	})
}

func TestGeneratedPagingCursorValues(t *testing.T) {
	for _, tc := range []struct {
		name                string
		required, defaulted bool
	}{
		{"required", true, false}, {"defaulted", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := buildCompleteGeneratedFiles(t, func() {
				pagingCompositionCursorDesign(tc.required, tc.defaulted)
			})
			root := writeCompleteGeneratedModule(t, files)
			writeGeneratedPackageTest(t, root, "gen/consumer/toolsets/records/cursor_test.go", `package records

import (
	"context"
	"testing"

	genprovider "generated.local/gen/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/toolregistry"
)

type cursorService struct{ calls []string }

func (s *cursorService) Read(_ context.Context, p *genprovider.Query) (*genprovider.Page, error) {
	s.calls = append(s.calls, p.Cursor)
	return &genprovider.Page{Returned: 1}, nil
}
func (s *cursorService) Start(context.Context, *genprovider.First) (*genprovider.Page, error) {
	return &genprovider.Page{}, nil
}
func (s *cursorService) Next(context.Context, *genprovider.Next) (*genprovider.Page, error) {
	return &genprovider.Page{}, nil
}
func TestCursorValues(t *testing.T) {
	service := &cursorService{}
	provider := NewProvider(service)
	for _, input := range []string{"{\"selection\":{\"kind\":\"a\"}}", "{\"selection\":{\"kind\":\"a\"},\"cursor\":\"next\"}"} {
		spec := SpecSame()
		value, err := spec.ExecutionPayloadCodec.FromJSON([]byte(input))
		require.NoError(t, err)
		encoded, err := spec.ExecutionPayloadCodec.ToJSON(value)
		require.NoError(t, err)
		assert.JSONEq(t, input, string(encoded))
		name := Same

		result, err := provider.HandleToolCall(toolregistry.WithToolUseID(context.Background(), "call"), toolregistry.ToolCallMessage{Tool: name, ToolUseID: "call", Payload: []byte(input), Meta: &toolregistry.ToolCallMeta{SessionID: "session"}})
		require.NoError(t, err)
		require.Nil(t, result.Error)

	}
}
`)
			runGeneratedPackageTest(t, root, "./gen/consumer/...")
		})
	}
}
