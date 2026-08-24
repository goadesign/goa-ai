// Package codegen lists the files written for one agent and writes the JSON document
// that describes all of its tools.
package codegen

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"goa.design/goa/v3/codegen"
)

// agentFiles returns all files written for one agent.
func agentFiles(agent *AgentData) ([]*codegen.File, error) {
	files := []*codegen.File{
		agentImplFile(agent),
		agentConfigFile(agent),
		agentRegistryFile(agent),
	}
	agg, err := agentSpecsAggregatorFile(agent)
	if err != nil {
		return nil, err
	}
	if agg != nil {
		files = append(files, agg)
	}
	if jsonFile := agentSpecsJSONFile(agent); jsonFile != nil {
		files = append(files, jsonFile)
	}
	files = append(files, agentToolsFiles(agent)...)
	files = append(files, agentToolsConsumerFiles(agent)...)
	files = append(files, mcpExecutorFiles(agent)...)
	files = append(files, usedToolsFiles(agent)...)
	serviceFiles, err := serviceExecutorFiles(agent)
	if err != nil {
		return nil, err
	}
	files = append(files, serviceFiles...)

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
//	      "server_data": {
//	        "name": "ServerDataType",
//	        "schema": { /* JSON Schema */ }
//	      }
//	    }
//	  ]
//	}
//
// Schemas are emitted only when available; tools without payload, result, or
// server data schemas still appear with their names.
func agentSpecsJSONFile(agent *AgentData) *codegen.File {
	data := agentToolSpecsData(agent)
	if len(data.tools) == 0 {
		return nil
	}

	type typeSchema struct {
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema,omitempty"`
	}

	type confirmationSchema struct {
		Title                string `json:"title,omitempty"`
		PromptTemplate       string `json:"prompt_template"`
		DeniedResultTemplate string `json:"denied_result_template"`
	}

	type serverDataSchema struct {
		Kind        string     `json:"kind"`
		Audience    string     `json:"audience"`
		Description string     `json:"description,omitempty"`
		Type        typeSchema `json:"type"`
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
		Payload      *typeSchema         `json:"payload,omitempty"`
		Result       *typeSchema         `json:"result,omitempty"`
		ServerData   []serverDataSchema  `json:"server_data,omitempty"`
	}

	out := struct {
		Tools []toolSchema `json:"tools"`
	}{
		Tools: make([]toolSchema, 0, len(data.tools)),
	}

	for _, t := range data.tools {
		if t == nil {
			continue
		}

		entry := toolSchema{
			ID:          t.Name,
			Service:     t.Service,
			Toolset:     t.Toolset,
			Title:       t.Title,
			Description: t.Description,
		}
		if len(t.Tags) > 0 {
			tags := make([]string, len(t.Tags))
			copy(tags, t.Tags)
			entry.Tags = tags
		}
		if len(t.Meta) > 0 {
			meta := make(map[string][]string, len(t.Meta))
			for k, vals := range t.Meta {
				cpy := make([]string, len(vals))
				copy(cpy, vals)
				meta[k] = cpy
			}
			entry.Meta = meta
		}

		if td := t.Payload; td != nil && td.TypeName != "" {
			ts := typeSchema{
				Name: td.TypeName,
			}
			if len(td.SchemaJSON) > 0 {
				ts.Schema = json.RawMessage(td.SchemaJSON)
			}
			entry.Payload = &ts
		}

		if td := t.Result; td != nil && td.TypeName != "" {
			ts := typeSchema{
				Name: td.TypeName,
			}
			if len(td.SchemaJSON) > 0 {
				ts.Schema = json.RawMessage(td.SchemaJSON)
			}
			entry.Result = &ts
		}

		if len(t.ServerData) > 0 {
			schemas := make([]serverDataSchema, 0, len(t.ServerData))
			for _, sd := range t.ServerData {
				if sd == nil || sd.Type == nil || sd.Type.TypeName == "" {
					continue
				}
				ts := typeSchema{
					Name: sd.Type.TypeName,
				}
				if len(sd.Type.SchemaJSON) > 0 {
					ts.Schema = json.RawMessage(sd.Type.SchemaJSON)
				}
				schemas = append(schemas, serverDataSchema{
					Kind:        sd.Kind,
					Audience:    sd.Audience,
					Description: sd.Description,
					Type:        ts,
				})
			}
			if len(schemas) > 0 {
				entry.ServerData = schemas
			}
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

	if len(out.Tools) == 0 {
		return nil
	}

	payload, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		panic(fmt.Errorf("encode tool schemas for agent %q: %w", agent.Name, err))
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
	}
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
