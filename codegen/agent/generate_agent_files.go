// Package codegen lists the files written for one agent and writes the JSON document
// that describes all of its tools.
package codegen

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	agentexpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/codegen"
)

type (
	// agentTypeSchema is the existing catalog's named JSON Schema representation.
	agentTypeSchema struct {
		// Name is the generated type name associated with the contract.
		Name string `json:"name"`
		// Schema is the compiler-produced JSON document when one is available.
		Schema json.RawMessage `json:"schema,omitempty"`
	}
	// agentInputBindingSchemas lists available compiled input contracts without
	// claiming which installer the application invokes or supplying its values.
	agentInputBindingSchemas struct {
		// Payload is the schema installed by ordinary application-value binding.
		Payload *agentTypeSchema `json:"payload"`
		// TextOnlyPayload is the ordinary text-only input when supported.
		TextOnlyPayload *agentTypeSchema `json:"text_only_payload,omitempty"`
		// LookupPayload is the key-shaped input declared by Lookup, when present.
		LookupPayload *agentTypeSchema `json:"lookup_payload,omitempty"`
		// LookupTextOnlyPayload is the text-only key-shaped input when supported.
		LookupTextOnlyPayload *agentTypeSchema `json:"lookup_text_only_payload,omitempty"`
	}
)

// agentFiles returns all files written for one agent.
func agentFiles(
	agent *AgentData,
	aggregates *aggregateSpecsPackagesPlan,
	bindings []*consumerInputPlan,
) ([]*codegen.File, error) {
	files := []*codegen.File{
		agentImplFile(agent),
		agentConfigFile(agent),
		agentRegistryFile(agent),
	}
	agg := agentSpecsAggregatorFile(aggregates.file(agent.ID))
	if agg != nil {
		files = append(files, agg)
	}
	jsonFile, err := agentSpecsJSONFile(agent, bindings)
	if err != nil {
		return nil, err
	}
	if jsonFile != nil {
		files = append(files, jsonFile)
	}
	files = append(files, agentToolsFiles(agent)...)
	files = append(files, agentToolsConsumerFiles(agent)...)

	var filtered []*codegen.File
	for _, f := range files {
		if f != nil {
			filtered = append(filtered, f)
		}
	}
	return filtered, nil
}

// agentSpecsJSONFile writes specs/tool_schemas.json with every tool name, input,
// output, and server data schema used by the agent.
//
// The JSON structure is:
//
//	{
//	  "tools": [
//	    {
//	      "id": "toolset.tool",
//	      "service": "svc",
//	      "toolset": "toolset",
//	      "title": "Title",
//	      "description": "Description",
//	      "tags": ["tag"],
//	      "payload": {
//	        "name": "PayloadType",
//	        "schema": { /* JSON Schema */ }
//	      },
//	      "result": {
//	        "name": "ResultType",
//	        "schema": { /* JSON Schema */ }
//	      },
//	      "server_data": [{
//	        "kind": "images.retained.v1",
//	        "audience": "evidence",
//	        "native_image": true,
//	        "type": {
//	          "name": "ServerDataType",
//	          "schema": { /* JSON Schema */ }
//	        }
//	      }]
//	    }
//	  ]
//	}
//
// Schemas are emitted only when available; tools without payload, result, or
// server data schemas still appear with their names.
func agentSpecsJSONFile(agent *AgentData, bindings []*consumerInputPlan) (*codegen.File, error) {
	selected, err := selectedAgentData(agent)
	if err != nil {
		return nil, err
	}
	agent = selected

	type confirmationSchema struct {
		Title                string `json:"title,omitempty"`
		PromptTemplate       string `json:"prompt_template"`
		DeniedResultTemplate string `json:"denied_result_template"`
	}

	type serverDataSchema struct {
		Kind        string          `json:"kind"`
		Audience    string          `json:"audience"`
		NativeImage bool            `json:"native_image,omitempty"`
		Description string          `json:"description,omitempty"`
		Type        agentTypeSchema `json:"type"`
	}

	type toolSchema struct {
		ID           string              `json:"id"`
		Service      string              `json:"service"`
		Toolset      string              `json:"toolset"`
		Title        string              `json:"title,omitempty"`
		Description  string              `json:"description,omitempty"`
		Tags         []string            `json:"tags,omitempty"`
		Meta         map[string][]string `json:"meta,omitempty"`
		Confirmation *confirmationSchema `json:"confirmation,omitempty"`
		Payload      *agentTypeSchema    `json:"payload,omitempty"`
		Result       *agentTypeSchema    `json:"result,omitempty"`
		// TextOnlyPayload retains the provider's generated UI-free contract.
		TextOnlyPayload *agentTypeSchema `json:"text_only_payload,omitempty"`
		// InputBinding lists only this exact reference's compiled input choices.
		InputBinding *agentInputBindingSchemas `json:"input_binding,omitempty"`
		ServerData   []serverDataSchema        `json:"server_data,omitempty"`
	}
	type catalogRoute struct {
		service string
		toolset string
	}
	// bindingKey joins the exact retained reference and authored tool name;
	// generated package spellings and registration-route guesses are unnecessary.
	type bindingKey struct {
		// reference is the exact expression retained by this toolset reference.
		reference *agentexpr.ToolsetExpr
		// tool is the authored name used by the provider's existing tool lookup.
		tool string
	}
	inputs := make(map[bindingKey]*consumerInputTool)
	for _, binding := range bindings {
		for _, tool := range binding.tools {
			inputs[bindingKey{binding.reference.Expr, tool.expression.Name}] = tool
		}
	}

	out := struct {
		// AgentID lets tooling associate this file without guessing package names.
		AgentID string       `json:"agent_id"`
		Tools   []toolSchema `json:"tools"`
	}{
		AgentID: agent.ID,
		Tools:   make([]toolSchema, 0, len(agent.Tools)),
	}

	// A shared specification package describes the tool contract once. Build
	// this agent's catalog from each local registration so its displayed service
	// and toolset match the route that this agent actually uses.
	seen := make(map[string]catalogRoute)
	for _, toolset := range agent.AllToolsets {
		if toolset == nil || toolset.specs == nil {
			continue
		}
		if toolset.SourceServiceName == "" {
			return nil, fmt.Errorf("agent %q toolset %q has no catalog service", agent.Name, toolset.QualifiedName)
		}
		tools, err := buildToolRenderData(toolset)
		if err != nil {
			return nil, err
		}
		for _, tool := range tools {
			t := tool.Spec

			route := catalogRoute{
				service: toolset.SourceServiceName,
				toolset: toolset.QualifiedName,
			}
			if previous, ok := seen[t.Name]; ok {
				if previous != route {
					return nil, fmt.Errorf(
						"agent %q registers tool %q through both %q and %q",
						agent.Name,
						t.Name,
						previous.toolset,
						route.toolset,
					)
				}
				continue
			}
			seen[t.Name] = route

			entry := toolSchema{
				ID:          t.Name,
				Service:     route.service,
				Toolset:     route.toolset,
				Title:       t.Title,
				Description: t.Description,
			}
			if len(t.Tags) > 0 {
				entry.Tags = append([]string(nil), t.Tags...)
			}
			if len(t.Meta) > 0 {
				meta := make(map[string][]string, len(t.Meta))
				for k, vals := range t.Meta {
					meta[k] = append([]string(nil), vals...)
				}
				entry.Meta = meta
			}

			entry.Payload = catalogTypeSchema(t.Payload)
			entry.Result = catalogTypeSchema(t.Result)
			entry.TextOnlyPayload = catalogTypeSchema(t.TextOnlyPayload)
			if bound := inputs[bindingKey{toolset.Expr, tool.Tool.Name}]; bound != nil {
				entry.InputBinding = &agentInputBindingSchemas{
					Payload:       catalogTypeSchema(bound.model),
					LookupPayload: catalogTypeSchema(bound.lookupModel),
				}
				if t.TextOnlyPayload != nil {
					entry.InputBinding.TextOnlyPayload = catalogTypeSchema(bound.textOnly)
					entry.InputBinding.LookupTextOnlyPayload = catalogTypeSchema(bound.lookupTextOnly)
				}
			}

			if len(t.ServerData) > 0 {
				schemas := make([]serverDataSchema, 0, len(t.ServerData))
				for _, sd := range t.ServerData {
					if sd == nil || sd.Type == nil || sd.Type.TypeName == "" {
						continue
					}
					ts := agentTypeSchema{Name: sd.Type.TypeName}
					if len(sd.Type.SchemaJSON) > 0 {
						ts.Schema = json.RawMessage(sd.Type.SchemaJSON)
					}
					schemas = append(schemas, serverDataSchema{
						Kind:        sd.Kind,
						Audience:    sd.Audience,
						NativeImage: sd.NativeImage,
						Description: sd.Description,
						Type:        ts,
					})
				}
				entry.ServerData = schemas
			}

			if c := t.Confirmation; c != nil {
				entry.Confirmation = &confirmationSchema{
					Title:                c.Title,
					PromptTemplate:       c.PromptTemplate,
					DeniedResultTemplate: c.DeniedResultTemplate,
				}
			}

			out.Tools = append(out.Tools, entry)
		}
	}

	if len(out.Tools) == 0 {
		return nil, nil
	}
	sort.Slice(out.Tools, func(left, right int) bool {
		return out.Tools[left].ID < out.Tools[right].ID
	})

	payload, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode tool schemas for agent %q: %w", agent.Name, err)
	}
	// End the file with a newline so text tools display it cleanly.
	payload = append(payload, '\n')

	sections := []*codegen.SectionTemplate{
		{
			Name:   "tool-schemas-json",
			Source: "{{ . }}",
			Data:   string(payload),
		},
	}
	path := filepath.Join(agent.Dir, "specs", "tool_schemas.json")
	return &codegen.File{
		Path:             path,
		SectionTemplates: sections,
	}, nil
}

// catalogTypeSchema preserves the existing catalog's type/schema availability
// rules while allowing every view to read the same linked schema data.
func catalogTypeSchema(data *typeData) *agentTypeSchema {
	if data == nil || data.TypeName == "" {
		return nil
	}
	result := &agentTypeSchema{Name: data.TypeName}
	if len(data.SchemaJSON) > 0 {
		result.Schema = json.RawMessage(data.SchemaJSON)
	}
	return result
}

// agentToolSpecsData combines the saved names, types, and schemas for every
// tool package used by agent.
func agentToolSpecsData(agent *AgentData) *toolSpecsData {
	data := newToolSpecsData(agent.Genpkg, agent.Service)
	seen := make(map[*toolSpecsData]struct{})
	for _, toolset := range agent.AllToolsets {
		if len(toolset.Tools) == 0 {
			continue
		}
		if _, ok := seen[toolset.specs]; ok {
			continue
		}
		seen[toolset.specs] = struct{}{}
		for _, tool := range toolset.specs.tools {
			data.addTool(tool)
		}
		for _, typ := range toolset.specs.typesList() {
			data.addType(typ)
		}
	}
	sort.Slice(data.tools, func(left, right int) bool {
		return data.tools[left].Name < data.tools[right].Name
	})
	return data
}
