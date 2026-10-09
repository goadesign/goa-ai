// Package mcpinput resolves typed job methods without changing their Goa
// attributes. Local executors, registry providers and MCP adapters consume the
// same job observation and declared questions; the service owns persistence.
package mcpinput

import (
	"fmt"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/expr"
)

type (
	// TaskBinding retains one creator's native job operations and observation.
	TaskBinding struct {
		// Creator starts durable work and returns its first observation.
		Creator *expr.MethodExpr
		// Read observes the same job without invoking its creator again.
		Read *expr.MethodExpr
		// Answer accepts host responses for the job's outstanding questions.
		Answer *expr.MethodExpr
		// Cancel acknowledges the request to cancel that job.
		Cancel *expr.MethodExpr
		// Observation retains the authored named result and its type location.
		Observation *expr.AttributeExpr
		// Metadata contains the job identity and per-observation retention facts.
		Metadata *expr.AttributeExpr
		// Outcome selects the observation's exact state.
		Outcome *expr.AttributeExpr
		// Complete is the domain value advertised to the model.
		Complete *expr.AttributeExpr
		// Pending contains typed requests for host input.
		Pending *expr.AttributeExpr
		// Failure contains an explicit JSON-RPC execution error.
		Failure *expr.AttributeExpr
		// Questions pairs declared requests with their typed answers.
		Questions []*Question
	}
)

// TaskExchangeMetaKey records the three native method names on the creator.
const TaskExchangeMetaKey = "agent:task:exchange"

// TaskExchange resolves the creator's three methods and complete observation.
// Ordinary methods return nil. Invalid shapes fail before names or codecs are
// generated, so no adapter can silently discard authored job data.
func TaskExchange(method *expr.MethodExpr) (*TaskBinding, error) {
	names, exists := method.Meta[TaskExchangeMetaKey]
	if !exists {
		return nil, nil
	}
	if len(names) != 3 {
		return nil, fmt.Errorf("TaskExchange requires read, answer and cancel method names")
	}
	if method.IsStreaming() {
		return nil, fmt.Errorf("TaskExchange requires a unary creator")
	}
	binding := &TaskBinding{Creator: method}
	seen := map[string]bool{method.Name: true}
	for i, target := range []**expr.MethodExpr{&binding.Read, &binding.Answer, &binding.Cancel} {
		name := names[i]
		operation := method.Service.Method(name)
		if name == "" || operation == nil || seen[name] {
			return nil, fmt.Errorf("TaskExchange method %q must name a distinct method in service %q", name, method.Service.Name)
		}
		if operation.IsStreaming() {
			return nil, fmt.Errorf("TaskExchange method %q must be unary", name)
		}
		if _, nested := operation.Meta[TaskExchangeMetaKey]; nested {
			return nil, fmt.Errorf("TaskExchange method %q cannot create another job", name)
		}
		input := Resolved(operation.Payload)
		payload := expr.AsObject(input.Type)
		if payload == nil || payload.Attribute("taskId") == nil || !input.IsRequired("taskId") || primitive(payload.Attribute("taskId").Type) != expr.String {
			return nil, fmt.Errorf("TaskExchange method %q requires a taskId string", name)
		}
		seen[name] = true
		*target = operation
	}
	binding.Observation = method.Result
	input, err := InputExchange(method)
	if err != nil {
		return nil, err
	}
	if input != nil {
		binding.Observation = input.Complete
	}
	if _, named := binding.Observation.Type.(expr.UserType); !named || binding.Observation.Type != binding.Read.Result.Type {
		return nil, fmt.Errorf("TaskExchange creator and read method must return the same named observation type")
	}
	if binding.Answer.Result.Type != expr.Empty || binding.Cancel.Result.Type != expr.Empty {
		return nil, fmt.Errorf("TaskExchange answer and cancel methods must return no domain result")
	}
	graph := newContractGraph(binding.Observation, binding.Answer.Payload)
	if err := binding.validateObservation(graph); err != nil {
		return nil, err
	}
	binding.restoreAuthoredFields(graph)
	return binding, nil
}

// validateObservation requires metadata and one state. The state discriminator
// derives the protocol status; metadata cannot contradict it with a second status.
func (b *TaskBinding) validateObservation(graph contractGraph) error {
	observation := graph.attribute(b.Observation)
	object := expr.AsObject(observation.Type)
	if object == nil || len(*object) != 2 || object.Attribute("task") == nil || object.Attribute("outcome") == nil || !observation.IsRequired("task") || !observation.IsRequired("outcome") {
		return fmt.Errorf("TaskExchange observation requires only task metadata and an outcome OneOf")
	}
	b.Metadata, b.Outcome = object.Attribute("task"), object.Attribute("outcome")
	if err := validateTaskMetadata(b.Metadata); err != nil {
		return err
	}
	choice := expr.AsUnion(b.Outcome.Type)
	if choice == nil || len(choice.Values) != 5 {
		return fmt.Errorf("TaskExchange outcome requires working, input_required, complete, failed and cancelled branches")
	}
	seen := make(map[string]bool, len(choice.Values))
	for _, branch := range choice.Values {
		if seen[branch.Name] {
			return fmt.Errorf("TaskExchange outcome repeats branch %q", branch.Name)
		}
		seen[branch.Name] = true
		switch branch.Name {
		case "working", "cancelled":
			fields := expr.AsObject(branch.Attribute.Type)
			if fields == nil || len(*fields) != 0 {
				return fmt.Errorf("TaskExchange %s branch must be an empty object", branch.Name)
			}
		case "complete":
			b.Complete = branch.Attribute
		case "input_required":
			b.Pending = branch.Attribute
		case "failed":
			b.Failure = branch.Attribute
		default:
			return fmt.Errorf("TaskExchange outcome has unknown branch %q", branch.Name)
		}
	}
	if b.Complete == nil || b.Pending == nil || b.Failure == nil {
		return fmt.Errorf("TaskExchange outcome is missing a required state")
	}
	if err := validateTaskFailure(b.Failure); err != nil {
		return err
	}
	pending := expr.AsObject(b.Pending.Type)
	if pending == nil || len(*pending) != 1 || pending.Attribute("requests") == nil || !b.Pending.IsRequired("requests") {
		return fmt.Errorf("TaskExchange input_required requires only a requests object")
	}
	answerInput := graph.attribute(b.Answer.Payload)
	answers := answerInput.Find("responses")
	if answers == nil || !answerInput.IsRequired("responses") {
		return fmt.Errorf("TaskExchange answer method requires a responses object")
	}
	questions, err := mapTaskQuestions(pending.Attribute("requests"), answers)
	if err != nil {
		return err
	}
	b.Questions = questions
	return nil
}

// mapTaskQuestions pairs the statically declared question kinds inside dynamic
// maps. The service owns each map key for the whole job; generation owns the
// exact request and answer types associated with its selected union branch.
func mapTaskQuestions(requests, responses *expr.AttributeExpr) ([]*Question, error) {
	requestMap, responseMap := expr.AsMap(requests.Type), expr.AsMap(responses.Type)
	if requestMap == nil || responseMap == nil || primitive(requestMap.KeyType.Type) != expr.String || primitive(responseMap.KeyType.Type) != expr.String {
		return nil, fmt.Errorf("TaskExchange requests and responses must be string-keyed typed maps")
	}
	requestValue, responseValue := requestMap.ElemType, responseMap.ElemType
	requestObject, responseObject := expr.AsObject(requestValue.Type), expr.AsObject(responseValue.Type)
	if requestObject == nil || responseObject == nil || len(*requestObject) != 1 || len(*responseObject) != 1 || requestObject.Attribute("request") == nil || responseObject.Attribute("response") == nil || !requestValue.IsRequired("request") || !responseValue.IsRequired("response") {
		return nil, fmt.Errorf("TaskExchange map values require only a request or response OneOf")
	}
	requestKinds, responseKinds := expr.AsUnion(requestObject.Attribute("request").Type), expr.AsUnion(responseObject.Attribute("response").Type)
	if requestKinds == nil || responseKinds == nil || len(requestKinds.Values) != len(responseKinds.Values) || len(requestKinds.Values) == 0 {
		return nil, fmt.Errorf("TaskExchange request and response unions must declare matching question kinds")
	}
	var questions []*Question
	seen := make(map[string]bool, len(requestKinds.Values))
	for _, request := range requestKinds.Values {
		var response *expr.AttributeExpr
		for _, candidate := range responseKinds.Values {
			if candidate.Name == request.Name {
				if response != nil {
					return nil, fmt.Errorf("TaskExchange repeats response kind %q", candidate.Name)
				}
				response = candidate.Attribute
			}
		}
		if request.Name == "" || seen[request.Name] || response == nil {
			return nil, fmt.Errorf("TaskExchange question kind %q requires one matching response", request.Name)
		}
		question, err := mapQuestion(request.Name, request.Attribute, response)
		if err != nil {
			return nil, fmt.Errorf("TaskExchange question kind: %w", err)
		}
		seen[request.Name] = true
		questions = append(questions, question)
	}
	return questions, nil
}

// validateTaskMetadata keeps identity and observation facts in one typed object.
// Optional integer retention means unlimited when absent; protocol encoding owns
// the always-present nullable JSON member rather than changing this domain type.
func validateTaskMetadata(attribute *expr.AttributeExpr) error {
	object := expr.AsObject(attribute.Type)
	if object == nil {
		return fmt.Errorf("TaskExchange task metadata must be an object")
	}
	for _, name := range []string{"taskId", "createdAt", "lastUpdatedAt"} {
		field := object.Attribute(name)
		if field == nil || !attribute.IsRequired(name) || primitive(field.Type) != expr.String {
			return fmt.Errorf("TaskExchange metadata requires %s as a string", name)
		}
	}
	for _, field := range *object {
		switch field.Name {
		case "taskId", "createdAt", "lastUpdatedAt":
		case "statusMessage":
			if attribute.IsRequired(field.Name) || primitive(field.Attribute.Type) != expr.String {
				return fmt.Errorf("TaskExchange statusMessage must be an optional string")
			}
		case "ttlMs", "pollIntervalMs":
			if attribute.IsRequired(field.Name) || primitive(field.Attribute.Type) != expr.Int64 {
				return fmt.Errorf("TaskExchange %s must be optional Int64 milliseconds", field.Name)
			}
		default:
			return fmt.Errorf("TaskExchange task metadata has unknown field %q", field.Name)
		}
	}
	return nil
}

// validateTaskFailure requires an explicit protocol error. Ordinary domain
// failures belong to completed tool results rather than the failed task state.
func validateTaskFailure(attribute *expr.AttributeExpr) error {
	object := expr.AsObject(attribute.Type)
	if object == nil {
		return fmt.Errorf("TaskExchange failed branch must be a JSON-RPC error object")
	}
	for _, name := range []string{"code", "message"} {
		field := object.Attribute(name)
		want := expr.DataType(expr.String)
		if name == "code" {
			want = expr.Int
		}
		if field == nil || !attribute.IsRequired(name) || primitive(field.Type) != want {
			return fmt.Errorf("TaskExchange failed branch requires %s", name)
		}
	}
	for _, field := range *object {
		switch field.Name {
		case "code", "message":
		case "data":
			if attribute.IsRequired("data") || primitive(field.Attribute.Type) != expr.Any {
				return fmt.Errorf("TaskExchange error data must be optional protocol JSON")
			}
			name, imported := codegen.GetMetaType(field.Attribute)
			if name != "rawjson.Message" || imported == nil || imported.Path != "goa.design/goa-ai/runtime/agent/rawjson" {
				return fmt.Errorf("TaskExchange error data requires rawjson.Message metadata to preserve exact JSON")
			}
		default:
			return fmt.Errorf("TaskExchange failed branch has unknown field %q", field.Name)
		}
	}
	return nil
}

// restoreAuthoredFields returns the original observation and question declarations.
// The creator and its read, answer and cancel methods keep their existing identity.
func (b *TaskBinding) restoreAuthoredFields(graph contractGraph) {
	b.Metadata = graph.original(b.Metadata)
	b.Outcome = graph.original(b.Outcome)
	b.Complete = graph.original(b.Complete)
	b.Pending = graph.original(b.Pending)
	b.Failure = graph.original(b.Failure)
	for _, question := range b.Questions {
		question.restoreAuthoredFields(graph)
	}
}
