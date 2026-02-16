package codegen

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"goa.design/goa/v3/codegen"
)

// agentFiles collects all files generated for a single agent.
func agentFiles(agent *AgentData) []*codegen.File {
	files := []*codegen.File{
		agentImplFile(agent),
		agentConfigFile(agent),
		agentRegistryFile(agent),
	}
	// Emit agent-level aggregator and embedded schemas; toolset specs/codecs are
	// generated separately once per owning toolset.
	if agg := agentSpecsAggregatorFile(agent); agg != nil {
		files = append(files, agg)
	}
	if jsonFile := agentSpecsJSONFile(agent); jsonFile != nil {
		files = append(files, jsonFile)
	}
	files = append(files, agentToolsFiles(agent)...)
	files = append(files, agentToolsConsumerFiles(agent)...)
	// Do not emit service toolset registrations; executors map tool payloads to service methods.
	files = append(files, mcpExecutorFiles(agent)...)
	// Emit typed helpers for Used toolsets (method-backed) to align planner UX.
	files = append(files, usedToolsFiles(agent)...)
	// Emit default service executor factories for method-backed Used toolsets.
	files = append(files, serviceExecutorFiles(agent)...)

	var filtered []*codegen.File
	for _, f := range files {
		if f != nil {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

// agentRouteRegisterFile emits Register<Agent>Route(ctx, rt) so caller processes can
// register route-only metadata for cross-process composition.
//
// agentRouteRegisterFile removed: agent-tools embed strong-contract route metadata in
// their generated toolset registrations, so no separate route registry is required.

// agentSpecsJSONFile emits specs/tool_schemas.json for an agent, capturing the
// JSON schemas for all tools declared across its toolsets. The file aggregates
// payload, result, and optional sidecar schemas in a backend-agnostic format
// that can be consumed by frontends or other tooling without depending on
// generated Go types.
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
//	      "sidecar": {
//	        "name": "SidecarType",
//	        "schema": { /* JSON Schema */ }
//	      }
//	    }
//	  ]
//	}
//
// Schemas are emitted only when available; tools without payload, result, or
// sidecar schemas still appear with name metadata so callers can rely on a
// stable catalogue.
func agentSpecsJSONFile(agent *AgentData) *codegen.File {
	data, err := buildToolSpecsData(agent)
	if err != nil {
		// Schema generation failures indicate a broken design or codegen bug and
		// must surface loudly so callers do not observe partial or drifting
		// tool catalogues. Fail generation instead of silently omitting schemas.
		panic(fmt.Errorf("goa-ai: tool schema generation failed for agent %q: %w", agent.Name, err))
	}
	if data == nil {
		return nil
	}
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
		ServerData   *typeSchema         `json:"server_data,omitempty"`
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

		if td := t.OptionalServerData; td != nil && td.TypeName != "" {
			ts := typeSchema{
				Name: td.TypeName,
			}
			if len(td.SchemaJSON) > 0 {
				ts.Schema = json.RawMessage(td.SchemaJSON)
			}
			entry.ServerData = &ts
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
		return nil
	}
	// Ensure a trailing newline for POSIX-friendly files and cleaner diffs.
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
