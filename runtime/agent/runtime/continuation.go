package runtime

// continuation.go keeps dedicated pagination correlation inside the runtime.
// The planner chooses among unfinished domain queries; the runtime binds the
// chosen source call and cursor before execution.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"goa.design/goa-ai/internal/registrycontract"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/schema"
)

const (
	continuationToolNamePrefix      = "continue_"
	continuationToolNameDigestBytes = 12
	continuationToolNameHexLength   = continuationToolNameDigestBytes * 2
)

type (
	// continuationInputMismatch identifies arguments that cannot execute under
	// a continuation's current codec. Historical actions may become unavailable;
	// current work still returns this error to its caller.
	continuationInputMismatch struct {
		error
	}

	// continuationAction is one model-visible action bound to one unfinished
	// bounded query. The executable tool and cursor remain runtime-owned.
	continuationAction struct {
		modelName         tools.Ident
		description       string
		spec              tools.ToolSpec
		state             continuationState
		executablePayload rawjson.Message
		registry          *tools.RegistryBinding
		definition        *model.ToolDefinition
		outputs           []*planner.ToolOutput
	}

	// continuationGroup keeps the ordered pages of one saved query together.
	continuationGroup struct {
		spec     tools.ToolSpec
		source   tools.ToolSpec
		outputs  []*planner.ToolOutput
		registry *tools.RegistryBinding
	}

	// continuationState is the successful page available to a dedicated
	// continuation action.
	continuationState struct {
		rootToolCallID string
		query          rawjson.Message
		cursor         string
		payload        rawjson.Message
		returned       int
	}
)

// IsGeneratedContinuationToolName reports whether name has the exact format
// reserved for runtime-generated pagination tools: "continue_" followed by 24
// lowercase hexadecimal characters. RegisterAgent and RegisterToolset reject
// authored tool names for which this function returns true.
func IsGeneratedContinuationToolName(name tools.Ident) bool {
	value := name.String()
	if len(value) != len(continuationToolNamePrefix)+continuationToolNameHexLength ||
		!strings.HasPrefix(value, continuationToolNamePrefix) {
		return false
	}
	for _, char := range value[len(continuationToolNamePrefix):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// Unwrap returns the generated codec's rejection so current callers can inspect
// the original input error while historical selection recognizes its category.
func (e continuationInputMismatch) Unwrap() error {
	return e.error
}

// availableContinuationActions returns one empty-input model action for every
// unfinished bounded query. Each action has a stable model name derived from
// the source tool call and executes the generated continuation tool.
func (r *Runtime) availableContinuationActions(agentID agent.Ident, outputs []*planner.ToolOutput, textOnly bool) ([]continuationAction, error) {
	var actions []continuationAction
	names := make(map[tools.Ident]struct{})
	groups, err := r.continuationGroups(agentID, outputs)
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		// Read every saved page before checking today's tools. A final page
		// closes its earlier cursor even when its old tool cannot execute now.
		states, err := continuationStates(group.spec, group.source, group.outputs)
		if err != nil {
			return nil, err
		}
		if len(states) == 0 {
			continue
		}
		available, err := r.selectContinuationExecution(agentID, &group)
		if err != nil {
			return nil, err
		}
		if !available {
			continue
		}
		spec := group.spec
		source := group.source
		if textOnly {
			if spec.RequiresUI || spec.Confirmation != nil || spec.TextOnly == nil ||
				source.RequiresUI || source.Confirmation != nil || source.TextOnly == nil {
				continue
			}
			if r.toolConfirmation != nil {
				if _, requires := r.toolConfirmation.Confirm[spec.Name]; requires {
					continue
				}
			}
			spec = spec.ForTextOnly()
			source = source.ForTextOnly()
		}
		for _, state := range states {
			if textOnly {
				state.query, err = modelVisibleContinuationQuery(source, state.query)
				if err != nil {
					return nil, err
				}
			}
			action, err := newContinuationAction(spec, state)
			if err != nil {
				return nil, err
			}
			action.executablePayload, err = continuationPayload(spec, state)
			if err != nil {
				return nil, fmt.Errorf("runtime: build continuation action %q: %w", action.modelName, err)
			}
			if _, exists := names[action.modelName]; exists {
				return nil, fmt.Errorf("runtime: duplicate continuation action name %q", action.modelName)
			}
			if _, exists := r.toolSpec(action.modelName); exists {
				return nil, fmt.Errorf("runtime: continuation action name %q conflicts with a registered tool", action.modelName)
			}
			names[action.modelName] = struct{}{}
			action.registry = group.registry
			action.outputs = group.outputs
			if group.registry != nil {
				action.definition, err = model.NewToolDefinitionFromSpec(spec)
				if err != nil {
					return nil, err
				}
			} else {
				action.definition = r.advertisedToolDefinitions([]tools.ToolSpec{spec}, compiledToolPolicy{})[0]
			}
			// Choosing an unfinished query supplies every page argument. Replace
			// the source's input contract without changing its cached definition.
			definition := *action.definition
			definition.Input, err = model.AdvertisedToolInputFromSchema(rawjson.Message(`{"type":"object","properties":{},"additionalProperties":false}`))
			if err != nil {
				return nil, err
			}
			action.definition = &definition
			actions = append(actions, action)
		}
	}
	return actions, nil
}

// continuationGroups reads each query from its original registration and joins
// its pages by the runtime-owned source call ID. Different queries remain
// separate even when their names, registrations and cursor bytes are equal.
func (r *Runtime) continuationGroups(agentID agent.Ident, outputs []*planner.ToolOutput) ([]continuationGroup, error) {
	var groups []continuationGroup
	var staticOutputs []*planner.ToolOutput
	for _, output := range outputs {
		if output != nil && output.Registry == nil {
			staticOutputs = append(staticOutputs, output)
		}
	}
	for _, spec := range r.ToolSpecsForAgent(agentID) {
		composed, err := isComposedContinuationSpec(spec)
		if err != nil {
			return nil, err
		}
		if !composed && !isDedicatedContinuationSpec(spec) {
			continue
		}
		sourceName := spec.Name
		if !composed {
			sourceName = spec.Bounds.Paging.SourceTool
		}
		source, ok := r.toolSpec(sourceName)
		if !ok {
			return nil, fmt.Errorf("continuation tool %q has no source contract", spec.Name)
		}
		groups = append(groups, continuationGroup{spec: spec, source: source, outputs: staticOutputs})
	}
	chains, _, err := historicalPagingChains(outputs)
	if err != nil {
		return nil, err
	}
	for _, output := range outputs {
		if output == nil || output.Registry == nil || output.ContinuationRootToolCallID != "" || output.Failure != nil {
			continue
		}
		chain := chains[output.ToolCallID]
		head := chain[len(chain)-1]
		if head.Bounds == nil || head.Bounds.NextCursor == nil {
			continue
		}
		resolved, err := registrycontract.Read(output.Registry, r.agentTools(agentID).registryDeclaration)
		if errors.Is(err, registrycontract.ErrCompiledContractChanged) {
			continue
		}
		if err != nil {
			return nil, err
		}
		spec, ok := resolved.Specs[output.Name]
		if !ok {
			return nil, fmt.Errorf("saved continuation has no contract for %q", output.Name)
		}
		if spec.Bounds == nil || spec.Bounds.Paging == nil || spec.Bounds.Paging.ContinueTool == "" {
			continue
		}
		continuation := resolved.Specs[spec.Bounds.Paging.ContinueTool]
		composed, err := isComposedContinuationSpec(continuation)
		if err != nil {
			return nil, err
		}
		if !composed && !isDedicatedContinuationSpec(continuation) {
			continue
		}
		source := continuation
		if !composed {
			source = resolved.Specs[continuation.Bounds.Paging.SourceTool]
		}
		binding, err := resolved.Select(output.Registry.Registry, continuation.Name)
		if err != nil {
			return nil, err
		}
		groups = append(groups, continuationGroup{spec: continuation, source: source, registry: binding, outputs: chain})
	}
	return groups, nil
}

// selectContinuationExecution checks an unfinished saved query against the
// consuming agent's current tools. Valid history with an unavailable tool pair
// contributes no action; malformed saved declarations remain errors. Matching
// static pairs use their current generated codecs and input preparation.
func (r *Runtime) selectContinuationExecution(agentID agent.Ident, group *continuationGroup) (bool, error) {
	if group.registry == nil {
		return true, nil
	}
	resolved, err := registrycontract.Read(group.registry, r.agentTools(agentID).registryDeclaration)
	if err != nil {
		return false, err
	}
	registration, ok := r.agentByID(agentID)
	if !ok || registration.Definition.registryTools == nil {
		return false, nil
	}
	definition := registration.Definition
	version := ""
	if resolved.Registered.Toolset.Version != nil {
		version = string(*resolved.Registered.Toolset.Version)
	}
	if !definition.registryTools.Allows(group.registry.Registry, resolved.Registered.Toolset.Name, version) {
		return false, nil
	}
	source, sourceStatic := definition.spec(group.source.Name)
	continuation, continuationStatic := definition.spec(group.spec.Name)
	if !sourceStatic && !continuationStatic {
		return true, nil
	}
	if !sourceStatic || !continuationStatic || resolved.Source != nil {
		return false, nil
	}
	for _, spec := range []tools.ToolSpec{source, continuation} {
		declaration, err := resolved.Declaration(spec.Name)
		if err != nil {
			return false, err
		}
		kind := staticServiceKind
		if spec.IsAgentTool {
			kind = staticAgentKind
		}
		if declaration.ConsumerContract.Kind != kind {
			return false, nil
		}
	}
	available, err := historicalPagingExecutable(group.outputs, map[tools.Ident]tools.ToolSpec{source.Name: source, continuation.Name: continuation})
	if err != nil || !available {
		return false, err
	}
	group.source, group.spec = source, continuation
	return true, nil
}

// compilePlannerToolCalls converts validated planner intent into runtime-owned
// execution calls. It preserves provider transcript identity and binds private
// continuation cursors without exposing either field to planner code.
func (r *Runtime) compilePlannerToolCalls(
	requests []planner.ToolRequest,
	actions []continuationAction,
	modelCalls map[string]model.ToolCall,
) ([]ToolCall, error) {
	return r.compilePlannerToolCallsForRun(run.Context{}, requests, actions, modelCalls)
}

// compilePlannerToolCallsForRun turns planner intent into runtime-owned calls.
// It preserves provider transcript IDs separately and derives every execution
// ID from the current run and planner attempt.
func (r *Runtime) compilePlannerToolCallsForRun(
	runCtx run.Context,
	requests []planner.ToolRequest,
	actions []continuationAction,
	modelCalls map[string]model.ToolCall,
) ([]ToolCall, error) {
	byName := make(map[tools.Ident]continuationAction, len(actions))
	for _, action := range actions {
		byName[action.modelName] = action
	}
	bound := make(map[tools.Ident]struct{})
	calls := make([]ToolCall, len(requests))
	for i, request := range requests {
		call := ToolCall{
			Name:            request.Name,
			Payload:         append(rawjson.Message(nil), request.Payload...),
			ToolCallID:      generateDeterministicToolCallID(runCtx.RunID, runCtx.TurnID, runCtx.Attempt, request.Name, i),
			ModelToolCallID: request.ModelToolCallID,
		}
		if source, ok := modelCalls[request.ModelToolCallID]; ok {
			call.ModelName = source.Name
			call.ModelPayload = append(rawjson.Message(nil), source.Payload...)
		} else if request.ModelToolCallID != "" {
			return nil, planner.NewOutputContractError(
				fmt.Errorf("planner tool %q supplied a model call ID that does not belong to its selected model response", request.Name),
			)
		}
		action, ok := byName[call.Name]
		if !ok {
			if spec, registered := r.toolSpec(call.Name); registered &&
				isDedicatedContinuationSpec(spec) && call.ModelToolCallID != "" {
				return nil, planner.NewOutputContractError(
					fmt.Errorf("runtime: canonical continuation tool %q is not model-callable", call.Name),
				)
			}
			calls[i] = call
			continue
		}
		if call.ModelToolCallID == "" {
			return nil, planner.NewOutputContractError(
				fmt.Errorf("runtime: generated continuation tool %q requires a model call ID", call.Name),
			)
		}
		if call.ModelName != call.Name {
			return nil, planner.NewOutputContractError(fmt.Errorf(
				"runtime: generated continuation tool %q uses the model call for %q",
				call.Name,
				call.ModelName,
			))
		}
		if !bytes.Equal(call.ModelPayload, call.Payload) {
			return nil, planner.NewOutputContractError(
				fmt.Errorf("runtime: generated continuation tool %q payload differs from its model call", call.Name),
			)
		}
		if _, exists := bound[call.Name]; exists {
			return nil, planner.NewOutputContractError(
				fmt.Errorf("runtime: continuation tool %q cannot be called more than once in one planner result", call.Name),
			)
		}
		bound[call.Name] = struct{}{}
		if err := validateEmptyContinuationPayload(call.Payload); err != nil {
			return nil, planner.NewOutputContractError(
				fmt.Errorf("runtime: continuation tool %q payload: %w", call.Name, err),
			)
		}
		call.Name = action.spec.Name
		call.Registry = action.registry.Clone()
		call.Payload = append(rawjson.Message(nil), action.executablePayload...)
		call.ContinuationRootToolCallID = action.state.rootToolCallID
		calls[i] = call
	}
	return calls, nil
}

// automaticContinuationPlan advances empty live pages before asking the model
// to make another decision. A zero-item page contains no semantic evidence, so
// the generated continuation and its runtime-owned cursor determine the only
// useful next action.
func (r *Runtime) automaticContinuationPlan(runCtx run.Context, actions []continuationAction) (*PlanResult, bool) {
	calls := make([]ToolCall, 0, len(actions))
	for _, action := range actions {
		if action.state.returned != 0 {
			continue
		}
		calls = append(calls, ToolCall{
			Name:                       action.spec.Name,
			Registry:                   action.registry.Clone(),
			ToolCallID:                 generateDeterministicToolCallID(runCtx.RunID, runCtx.TurnID, runCtx.Attempt, action.spec.Name, len(calls)),
			Payload:                    append(rawjson.Message(nil), action.executablePayload...),
			ContinuationRootToolCallID: action.state.rootToolCallID,
		})
	}
	if len(calls) == 0 {
		return nil, false
	}
	return &PlanResult{ToolCalls: calls}, true
}

// continuationStates reconstructs one live head per source tool call. A
// continuation result carries its source call identity explicitly, so equal
// opaque cursors and repeated identical queries remain independent.
func continuationStates(spec, sourceSpec tools.ToolSpec, outputs []*planner.ToolOutput) ([]continuationState, error) {
	states := make(map[string]continuationState)
	var order []string
	for _, output := range outputs {
		if output == nil || output.Failure != nil || !isContinuationOutput(spec, output.Name) {
			continue
		}

		var rootToolCallID string
		var query rawjson.Message
		var consumedCursor string
		hasNextCursor := output.Bounds != nil && output.Bounds.NextCursor != nil && *output.Bounds.NextCursor != ""
		page := output.Name != sourceSpec.Name
		if spec.Name == sourceSpec.Name {
			completedSource := false
			if !hasNextCursor && output.ContinuationRootToolCallID == "" {
				// A saved first-page result with no next page needs no decoding
				// against today's tool input. Check for the cursor field so a final
				// continuation page still receives the usual validation.
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(output.Payload, &fields); err != nil {
					return nil, fmt.Errorf("runtime: paging tool %q history input: %w", spec.Name, err)
				}
				_, page = fields[spec.Bounds.Paging.CursorField]
				completedSource = !page
			}
			if !completedSource {
				var err error
				page, err = continuationPageInput(spec, output.Payload)
				if err != nil {
					return nil, fmt.Errorf("runtime: paging tool %q history input: %w", spec.Name, err)
				}
			}
		}
		if !page {
			if output.ContinuationRootToolCallID != "" {
				return nil, fmt.Errorf("runtime: continuation source tool %q history unexpectedly names a prior source call", output.Name)
			}
			if output.ToolCallID == "" {
				return nil, fmt.Errorf("runtime: continuation source tool %q history has an empty tool call id", output.Name)
			}
			rootToolCallID = output.ToolCallID
			if !hasNextCursor {
				delete(states, rootToolCallID)
				continue
			}
			var err error
			query, err = modelVisibleContinuationQuery(sourceSpec, output.Payload)
			if err != nil {
				return nil, fmt.Errorf("runtime: continuation source tool %q query: %w", output.Name, err)
			}
			if _, exists := states[rootToolCallID]; !exists {
				order = append(order, rootToolCallID)
			}
		} else {
			rootToolCallID = output.ContinuationRootToolCallID
			if rootToolCallID == "" {
				if output.ModelToolCallID == "" {
					// Planner code called this hidden tool directly. Its result does
					// not belong to a saved source query, so it creates no follow-up.
					continue
				}
				return nil, fmt.Errorf("runtime: continuation tool %q history has no source tool call id", spec.Name)
			}
			previous, exists := states[rootToolCallID]
			if !exists {
				return nil, fmt.Errorf(
					"runtime: continuation tool %q history references unknown source tool call %q",
					spec.Name,
					rootToolCallID,
				)
			}
			inputCursor, err := continuationInputCursor(spec, output.Payload)
			if err != nil {
				return nil, fmt.Errorf("runtime: continuation tool %q history: %w", spec.Name, err)
			}
			if inputCursor != previous.cursor {
				return nil, fmt.Errorf(
					"runtime: continuation tool %q history advanced source tool call %q with the wrong cursor",
					spec.Name,
					rootToolCallID,
				)
			}
			consumedCursor = inputCursor
			query = previous.query
		}

		if !hasNextCursor {
			delete(states, rootToolCallID)
			continue
		}
		if consumedCursor != "" && *output.Bounds.NextCursor == consumedCursor {
			return nil, fmt.Errorf(
				"runtime: continuation tool %q did not advance source tool call %q cursor",
				spec.Name,
				rootToolCallID,
			)
		}
		states[rootToolCallID] = continuationState{
			rootToolCallID: rootToolCallID,
			query:          query,
			cursor:         *output.Bounds.NextCursor,
			payload:        output.Payload,
			returned:       output.Bounds.Returned,
		}
	}

	live := make([]continuationState, 0, len(states))
	for _, rootToolCallID := range order {
		if state, ok := states[rootToolCallID]; ok {
			live = append(live, state)
		}
	}
	return live, nil
}

// newContinuationAction builds the model-facing identity and description for one
// exact live chain without exposing its cursor.
func newContinuationAction(spec tools.ToolSpec, state continuationState) (continuationAction, error) {
	if state.rootToolCallID == "" {
		return continuationAction{}, fmt.Errorf("runtime: continuation tool %q state has an empty source tool call id", spec.Name)
	}
	name := continuationActionName(spec.Name, state.rootToolCallID)
	sourceName := spec.Name
	if spec.Bounds.Paging.SourceTool != "" {
		sourceName = spec.Bounds.Paging.SourceTool
	}
	return continuationAction{
		modelName: name,
		description: fmt.Sprintf(
			"Continue the unfinished %s query with original input %s. The latest page returned %d items.",
			sourceName,
			state.query,
			state.returned,
		),
		spec:  spec,
		state: state,
	}, nil
}

// continuationActionName derives a provider-safe stable name from the canonical
// continuation tool and the source query's tool-call identity.
func continuationActionName(toolName tools.Ident, rootToolCallID string) tools.Ident {
	sum := sha256.Sum256([]byte(toolName.String() + "\x00" + rootToolCallID))
	return tools.Ident(
		continuationToolNamePrefix +
			hex.EncodeToString(sum[:continuationToolNameDigestBytes]),
	)
}

// modelVisibleContinuationQuery retains only generated model-facing fields
// from a canonical source payload. Runtime-injected fields therefore never
// enter dynamic tool descriptions.
func modelVisibleContinuationQuery(spec tools.ToolSpec, payload rawjson.Message) (rawjson.Message, error) {
	if len(spec.Payload.Fields) == 0 {
		return rawjson.Message(`{}`), nil
	}
	var canonical map[string]json.RawMessage
	if err := json.Unmarshal(payload, &canonical); err != nil {
		return nil, fmt.Errorf("decode canonical payload: %w", err)
	}
	visible := make(map[string]json.RawMessage)
	for _, field := range spec.Payload.Fields {
		if len(field.Path) != 1 {
			continue
		}
		name, ok := field.Path[0].(tools.FixedField)
		if !ok {
			continue
		}
		if value, ok := canonical[string(name)]; ok {
			visible[string(name)] = value
		}
	}
	data, err := json.Marshal(visible)
	if err != nil {
		return nil, fmt.Errorf("encode model-visible payload: %w", err)
	}
	return rawjson.Message(data), nil
}

// continuationInputCursor reads the runtime-authored cursor from a canonical
// continuation payload so history can identify the exact predecessor page.
func continuationInputCursor(spec tools.ToolSpec, payload rawjson.Message) (string, error) {
	fields := make(map[string]json.RawMessage)
	if err := json.Unmarshal(payload, &fields); err != nil {
		return "", fmt.Errorf("decode canonical payload: %w", err)
	}
	raw, ok := fields[spec.Bounds.Paging.CursorField]
	if !ok {
		return "", fmt.Errorf("canonical payload is missing cursor field %q", spec.Bounds.Paging.CursorField)
	}
	var cursor string
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return "", fmt.Errorf("decode cursor field %q: %w", spec.Bounds.Paging.CursorField, err)
	}
	if cursor == "" {
		return "", fmt.Errorf("canonical payload cursor field %q is empty", spec.Bounds.Paging.CursorField)
	}
	return cursor, nil
}

// isContinuationOutput reports whether a tool result belongs to the result set
// advanced by spec.
func isContinuationOutput(spec tools.ToolSpec, output tools.Ident) bool {
	return output == spec.Name || output == spec.Bounds.Paging.SourceTool
}

// continuationPayload copies the declared fields from the exact prior call,
// adds its next cursor, and returns input validated by the continuation codec.
func continuationPayload(spec tools.ToolSpec, state continuationState) (rawjson.Message, error) {
	fields := make(map[string]json.RawMessage)
	if len(spec.Bounds.Paging.RetainedFields) > 0 {
		var previous map[string]json.RawMessage
		if err := json.Unmarshal(state.payload, &previous); err != nil {
			return nil, fmt.Errorf("decode prior query payload: %w", err)
		}
		for _, name := range spec.Bounds.Paging.RetainedFields {
			if value, present := previous[name]; present {
				fields[name] = value
			}
		}
	}
	cursor, err := json.Marshal(state.cursor)
	if err != nil {
		return nil, fmt.Errorf("encode cursor: %w", err)
	}
	fields[spec.Bounds.Paging.CursorField] = cursor
	payload, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode query payload: %w", err)
	}
	value, err := spec.ExecutionPayloadCodec.FromJSON(payload)
	if err != nil {
		return nil, continuationInputMismatch{fmt.Errorf("decode retained query with generated codec: %w", err)}
	}
	canonical, err := spec.ExecutionPayloadCodec.ToJSON(value)
	if err != nil {
		return nil, fmt.Errorf("encode retained query with generated codec: %w", err)
	}
	return canonical, nil
}

// plannerContinuationInput distinguishes a complete code-authored page from a
// first input that still needs runtime-injected fields. Ordinary tools do not
// enter either paging codec; their usual input preparation remains unchanged.
func plannerContinuationInput(spec tools.ToolSpec, payload rawjson.Message) (bool, error) {
	if isDedicatedContinuationSpec(spec) {
		return continuationPageInput(spec, payload)
	}
	composed, err := isComposedContinuationSpec(spec)
	if err != nil || !composed {
		return false, err
	}
	if _, err := spec.Payload.Codec.FromJSON(payload); err == nil {
		return false, nil
	}
	return continuationPageInput(spec, payload)
}

// continuationPageInput validates a complete page input before identifying its
// branch. Separate continuation tools always use their execution codec; composed
// operations require the checked two-branch contract before cursor presence has
// meaning. Ordinary tools and older flat self references are not page inputs.
func continuationPageInput(spec tools.ToolSpec, payload rawjson.Message) (bool, error) {
	composed, err := isComposedContinuationSpec(spec)
	if err != nil {
		return false, err
	}
	if !composed && !isDedicatedContinuationSpec(spec) {
		return false, nil
	}
	if _, err := spec.ExecutionPayloadCodec.FromJSON(payload); err != nil {
		return false, err
	}
	if !composed {
		return true, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return false, err
	}
	_, page := fields[spec.Bounds.Paging.CursorField]
	return page, nil
}

// isComposedContinuationSpec checks the declared execution contract before
// a self reference can create page actions. Older flat self references therefore
// keep their original behavior and never acquire inferred input branches.
func isComposedContinuationSpec(spec tools.ToolSpec) (bool, error) {
	if spec.Bounds == nil || spec.Bounds.Paging == nil ||
		spec.Bounds.Paging.ContinueTool != spec.Name || spec.Bounds.Paging.SourceTool != "" {
		return false, nil
	}
	return schema.PagingComposition(spec.ExecutionPayloadSchema, spec.Bounds.Paging.CursorField)
}

// isDedicatedContinuationSpec identifies the no-argument action generated for
// a separate ContinueWith target.
func isDedicatedContinuationSpec(spec tools.ToolSpec) bool {
	return spec.Bounds != nil &&
		spec.Bounds.Paging != nil &&
		spec.Bounds.Paging.ContinueTool == spec.Name &&
		spec.Bounds.Paging.SourceTool != ""
}

// validateEmptyContinuationPayload rejects any model-authored argument because
// the advertised continuation contract is an empty object.
func validateEmptyContinuationPayload(payload rawjson.Message) error {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("must be an empty JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var value struct{}
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
