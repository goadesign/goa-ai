// Package codegen writes one native job dispatch flow for local executors and
// registry providers. The selected method receives copied creator context and
// the saved job ID; only a completed read reaches normal tool result encoding.
package codegen

import (
	"bytes"
	"fmt"
	"text/template"

	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

type (
	// nativeTaskCallData retains the final method names and converters for a job.
	nativeTaskCallData struct {
		Created, Read, ObservationRef, CompletedRef, API string
		Roles                                            [3]*nativeTaskRoleData
		binding                                          *mcpinput.TaskBinding
		methods                                          [3]*expr.MethodExpr
	}
	// nativeTaskDispatchData selects caller and return syntax before generation.
	nativeTaskDispatchData struct {
		Tool                          *ToolData
		Specs, Failure, CreatorCaller string
		Registry                      bool
		Continuation, TextOnly        string
	}
)

// callData gives each generated caller the observation and completed types in
// its own package while retaining the shared conversion declarations.
func (p *nativeTaskPlan) callData(types codegen.Attributor, api string) *nativeTaskCallData {
	data := &nativeTaskCallData{
		Created: p.created.Name(), Read: p.read.Name(),
		ObservationRef: types.Ref(p.binding.Observation, ""),
		CompletedRef:   types.Ref(p.binding.Complete, ""),
		API:            api, binding: p.binding,
		methods: [3]*expr.MethodExpr{p.binding.Read, p.binding.Answer, p.binding.Cancel},
	}
	for index, role := range p.roles {
		data.Roles[index] = &role.data
	}
	return data
}

// nativeTaskDispatchSource specializes the same job control flow for the
// registry's typed service and the local executor's existing method callers.
func nativeTaskDispatchSource(tool *ToolData, specs, failure, creatorCaller string, registry bool) (string, error) {
	data := nativeTaskDispatchData{Tool: tool, Specs: specs, Failure: failure, Registry: registry, CreatorCaller: creatorCaller, Continuation: "call.ExecutionContinuation", TextOnly: "call.TextOnly"}
	if registry {
		data.Continuation = "msg.Meta.ExecutionContinuation"
		data.TextOnly = "msg.Meta.TextOnly"
	}
	if specs != "" {
		data.Specs += "."
	}
	parsed, err := template.New(nativeTaskDispatchT).Parse(agentsTemplates.Read(nativeTaskDispatchT))
	if err != nil {
		return "", fmt.Errorf("parse native job dispatch: %w", err)
	}
	var output bytes.Buffer
	if err := parsed.Execute(&output, data); err != nil {
		return "", fmt.Errorf("render native job dispatch: %w", err)
	}
	return output.String(), nil
}
