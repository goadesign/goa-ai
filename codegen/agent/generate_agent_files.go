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
func agentFiles(agent *AgentData, aggregates *aggregateSpecsPackagesPlan) ([]*codegen.File, error) {
	files := []*codegen.File{
		agentImplFile(agent),
		agentConfigFile(agent),
		agentRegistryFile(agent),
	}
	agg := agentSpecsAggregatorFile(aggregates.file(agent.ID))
	if agg != nil {
		files = append(files, agg)
	}
	jsonFile, err := agentSpecsJSONFile(agent)
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
func agentSpecsJSONFile(agent *AgentData) (*codegen.File, error) {
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
	type catalogRoute struct {
		service string
		toolset string
	}

	out := struct {
		Tools []toolSchema `json:"tools"`
	}{
		Tools: make([]toolSchema, 0, len(agent.Tools)),
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
		for _, t := range toolset.specs.tools {
			if t == nil {
				continue
			}

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
					ts := typeSchema{Name: sd.Type.TypeName}
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
