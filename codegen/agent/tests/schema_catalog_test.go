// These tests read the established per-agent schema catalog as design tooling.
// They compare its variants with real generated and installed specifications.
package tests

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	goacodegen "goa.design/goa/v3/codegen"
	. "goa.design/goa/v3/dsl"
)

type (
	// schemaCatalog is the existing generated catalog with its consumer identity.
	schemaCatalog struct {
		// AgentID supplies the consumer identity directly from generated content.
		AgentID string `json:"agent_id"`
		// Tools contains the agent's existing selected tool records.
		Tools []catalogTool `json:"tools"`
	}
	// catalogType retains a named JSON Schema exactly as the catalog encodes it.
	catalogType struct {
		// Name identifies the corresponding generated Go type.
		Name string `json:"name"`
		// Schema holds the actual compiled JSON contract.
		Schema json.RawMessage `json:"schema"`
	}
	// catalogTool keeps the established route and provider schemas plus input views.
	catalogTool struct {
		// Name is the authored name supplied directly by the compiler.
		Name string `json:"name"`
		// ID is the existing qualified runtime tool identifier.
		ID string `json:"id"`
		// Toolset is the original selected registration route.
		Toolset string `json:"toolset"`
		// Payload is the unbound provider input contract.
		Payload *catalogType `json:"payload"`
		// HiddenFields lists fields supplied outside the model input.
		HiddenFields []string `json:"hidden_fields"`
		// Result is the provider's model-visible result contract.
		Result *catalogType `json:"result"`
		// TextOnlyPayload is present only for an available text-only view.
		TextOnlyPayload *catalogType `json:"text_only_payload"`
		// InputBinding contains this consumer's ordinary and lookup views.
		InputBinding *catalogBinding `json:"input_binding"`
	}
	// catalogBinding mirrors the available ordinary and lookup contracts.
	catalogBinding struct {
		// Payload is the input installed by ordinary binding.
		Payload *catalogType `json:"payload"`
		// TextOnlyPayload is the ordinary UI-free input when supported.
		TextOnlyPayload *catalogType `json:"text_only_payload"`
		// LookupPayload is the declared lookup-key input when available.
		LookupPayload *catalogType `json:"lookup_payload"`
		// LookupTextOnlyPayload is the corresponding UI-free lookup input.
		LookupTextOnlyPayload *catalogType `json:"lookup_text_only_payload"`
	}
)

// readSchemaCatalogs identifies catalogs through their documented file path,
// then reads identities from their content without interpreting directory names.
func readSchemaCatalogs(t *testing.T, files []*goacodegen.File) map[string]schemaCatalog {
	t.Helper()
	result := make(map[string]schemaCatalog)
	for _, file := range files {
		if !strings.HasSuffix(filepath.ToSlash(file.Path), "/specs/tool_schemas.json") {
			continue
		}
		var catalog schemaCatalog
		require.NoError(t, json.Unmarshal([]byte(testhelpers.FileContent(t, files, file.Path)), &catalog))
		require.NotEmpty(t, catalog.AgentID)
		require.NotContains(t, result, catalog.AgentID)
		result[catalog.AgentID] = catalog
	}
	return result
}

// TestSchemaCatalogPreservesNamesAndConsumerVariants checks exact runtime tool
// identities even when authored names map to the same preferred Go spelling.
func TestSchemaCatalogPreservesNamesAndConsumerVariants(t *testing.T) {
	files := testhelpers.BuildAndGenerateWithPkg(t, "generated.local/gen", func() {
		API("records", func() {})
		provider := Toolset("records", func() {
			for _, name := range []string{"read_orders", "readOrders"} {
				Tool(name, "Read orders.", func() {
					Args(func() {
						Attribute("orderID", String, "Requested order.")
						Attribute("scope", String, "Application scope.", func() { MinLength(3) })
						Attribute("render", Boolean, "Visual option.")
						Required("orderID", "scope")
					})
					Return(String)
					UIOnly("render")
					if name == "readOrders" {
						RequiresUI()
					}
				})
			}
		})
		Service("reader", func() {
			Agent("bound", "Bound reader.", func() { Use(provider, func() { BindField("scope") }) })
			Agent("lookup", "Lookup reader.", func() {
				Use(provider, func() {
					BindField("scope", func() { Lookup(String, "Scope key.", func() { MinLength(2) }) })
				})
			})
		})
	})
	catalogs := readSchemaCatalogs(t, files)
	require.Len(t, catalogs, 2)
	for id, catalog := range catalogs {
		require.Len(t, catalog.Tools, 2)
		assert.Equal(t, "records.readOrders", catalog.Tools[0].ID)
		assert.Equal(t, "records.read_orders", catalog.Tools[1].ID)
		for _, tool := range catalog.Tools {
			assert.Contains(t, []string{"read_orders", "readOrders"}, tool.Name)
			assert.Equal(t, "reader.records", tool.Toolset)
			require.NotNil(t, tool.Payload)
			assert.Contains(t, string(tool.Payload.Schema), `"order_id"`)
			assert.NotContains(t, string(tool.Payload.Schema), `"orderID"`)
			assert.Contains(t, string(tool.Payload.Schema), `"render"`)
			require.NotNil(t, tool.InputBinding)
			assert.NotContains(t, string(tool.InputBinding.Payload.Schema), `"scope"`)
			if tool.ID == "records.readOrders" {
				assert.Nil(t, tool.TextOnlyPayload)
				assert.Nil(t, tool.InputBinding.TextOnlyPayload)
				assert.Nil(t, tool.InputBinding.LookupTextOnlyPayload)
			} else {
				require.NotNil(t, tool.TextOnlyPayload)
				require.NotNil(t, tool.InputBinding.TextOnlyPayload)
				assert.NotContains(t, string(tool.TextOnlyPayload.Schema), `"render"`)
			}
			if id == "reader.lookup" {
				require.NotNil(t, tool.InputBinding.LookupPayload)
				assert.Contains(t, string(tool.InputBinding.LookupPayload.Schema), `"minLength": 2`)
				if tool.ID == "records.read_orders" {
					require.NotNil(t, tool.InputBinding.LookupTextOnlyPayload)
					assert.NotContains(t, string(tool.InputBinding.LookupTextOnlyPayload.Schema), `"render"`)
				}
			} else {
				assert.Equal(t, "reader.bound", id)
				assert.Nil(t, tool.InputBinding.LookupPayload)
				assert.Nil(t, tool.InputBinding.LookupTextOnlyPayload)
			}
		}
	}
}
