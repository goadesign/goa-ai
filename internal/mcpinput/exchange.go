// Package mcpinput reads an authored method's typed continuation and outcome fields.
// The same mapping supplies MCP adapters and their generated agent contracts.
// Goa still owns the service types and all field-level validation;
// this mapping rejects shapes that would lose data during protocol conversion.
package mcpinput

import (
	"fmt"

	"goa.design/goa/v3/expr"
)

type (
	// Exchange retains the authored fields for one operation's input rounds.
	// State belongs to that operation's service; callers never interpret it.
	Exchange struct {
		// ContinuationName selects the optional payload field excluded from model input.
		ContinuationName string
		// OutcomeName selects the required service result union.
		OutcomeName string
		// Continuation retains the original native payload field and its type location.
		Continuation *expr.AttributeExpr
		// Responses contains optional answers indexed by declared request identifiers.
		Responses *expr.AttributeExpr
		// Outcome retains the native completed-or-unfinished union.
		Outcome *expr.AttributeExpr
		// Complete supplies the finished value advertised in catalogs.
		Complete *expr.AttributeExpr
		// Pending supplies requests and opaque service-owned state.
		Pending *expr.AttributeExpr
		// Requests contains optional questions selected by the service.
		Requests *expr.AttributeExpr
		// Questions pairs requests and answers in authored request order.
		Questions []*Question
	}
	// Question pairs one service-owned request with its typed host response.
	// A nil Content denotes URL consent; form content supplies the form schema.
	Question struct {
		// Name is one identifier within the originating operation's input round.
		Name string
		// Request retains the typed message and optional URL declaration.
		Request *expr.AttributeExpr
		// Response retains the typed accept, decline and cancel declaration.
		Response *expr.AttributeExpr
		// Answer is the native response union selected by the host.
		Answer *expr.AttributeExpr
		// Content supplies accepted form fields; URL consent has no content.
		Content *expr.AttributeExpr
	}
)

// ExchangeMetaKey records the two authored field names without copying types.
const ExchangeMetaKey = "mcp:input:exchange"

// InputExchange resolves one method's mapping and checks its complete shape.
// Ordinary methods return nil; invalid declarations fail before code generation.
func InputExchange(method *expr.MethodExpr) (*Exchange, error) {
	names, declared := method.Meta[ExchangeMetaKey]
	if !declared {
		return nil, nil
	}
	if len(names) != 2 || names[0] == "" || names[1] == "" {
		return nil, fmt.Errorf("InputExchange requires continuation and outcome field names")
	}
	if method.IsStreaming() {
		return nil, fmt.Errorf("InputExchange requires a unary method")
	}
	graph := newContractGraph(method.Payload, method.Result)
	payloadAttribute := graph.attribute(method.Payload)
	resultAttribute := graph.attribute(method.Result)
	payload := expr.AsObject(payloadAttribute.Type)
	if payload == nil || payload.Attribute(names[0]) == nil || payloadAttribute.IsRequired(names[0]) {
		return nil, fmt.Errorf("InputExchange continuation %q must be an optional payload object", names[0])
	}
	mapping := &Exchange{ContinuationName: names[0], OutcomeName: names[1], Continuation: payload.Attribute(names[0])}
	if err := exchangeEnvelope(mapping.Continuation, "responses"); err != nil {
		return nil, fmt.Errorf("InputExchange continuation: %w", err)
	}
	mapping.Responses = expr.AsObject(mapping.Continuation.Type).Attribute("responses")
	result := expr.AsObject(resultAttribute.Type)
	if result == nil || len(*result) != 1 || result.Attribute(names[1]) == nil || !resultAttribute.IsRequired(names[1]) {
		return nil, fmt.Errorf("InputExchange result must contain only required outcome %q", names[1])
	}
	mapping.Outcome = result.Attribute(names[1])
	choice := expr.AsUnion(mapping.Outcome.Type)
	if choice == nil || len(choice.Values) != 2 {
		return nil, fmt.Errorf("InputExchange outcome requires complete and input_required OneOf branches")
	}
	for _, branch := range choice.Values {
		switch branch.Name {
		case "complete":
			mapping.Complete = branch.Attribute
		case "input_required":
			mapping.Pending = branch.Attribute
		default:
			return nil, fmt.Errorf("InputExchange outcome has unknown branch %q", branch.Name)
		}
	}
	if mapping.Complete == nil || mapping.Pending == nil {
		return nil, fmt.Errorf("InputExchange outcome requires complete and input_required OneOf branches")
	}
	if err := exchangeEnvelope(mapping.Pending, "requests"); err != nil {
		return nil, fmt.Errorf("InputExchange input_required: %w", err)
	}
	mapping.Requests = expr.AsObject(mapping.Pending.Type).Attribute("requests")
	pendingState := expr.AsObject(mapping.Pending.Type).Attribute("state")
	continuationState := expr.AsObject(mapping.Continuation.Type).Attribute("state")
	if (pendingState == nil) != (continuationState == nil) {
		return nil, fmt.Errorf("InputExchange state must be declared in both pending result and continuation")
	}
	if pendingState == nil && mapping.Requests == nil {
		return nil, fmt.Errorf("InputExchange input_required must declare state or requests")
	}
	if err := mapQuestions(mapping); err != nil {
		return nil, err
	}
	mapping.restoreAuthoredFields(graph)
	return mapping, nil
}

// CompleteResult selects the finished service value for catalogs and model
// codecs. The full native outcome remains the service and transport contract.
func CompleteResult(method *expr.MethodExpr) (*expr.AttributeExpr, error) {
	task, err := TaskExchange(method)
	if err != nil {
		return nil, err
	}
	if task != nil {
		return task.Complete, nil
	}
	mapping, err := InputExchange(method)
	if err != nil || mapping == nil {
		return method.Result, err
	}
	return mapping.Complete, nil
}

// exchangeEnvelope accepts optional state and optional typed requests or
// responses. Presence is checked on actual rounds, not inferred from slice shape.
func exchangeEnvelope(attribute *expr.AttributeExpr, collection string) error {
	object := expr.AsObject(attribute.Type)
	if object == nil {
		return fmt.Errorf("must be an object containing optional state and %s", collection)
	}
	for _, field := range *object {
		if attribute.IsRequired(field.Name) {
			return fmt.Errorf("%s must be optional", field.Name)
		}
		switch field.Name {
		case "state":
			if primitive(field.Attribute.Type) != expr.String {
				return fmt.Errorf("state must be a string")
			}
		case collection:
			if expr.AsObject(field.Attribute.Type) == nil {
				return fmt.Errorf("%s must be an object of declared question identifiers", collection)
			}
		default:
			return fmt.Errorf("unknown field %q", field.Name)
		}
	}
	return nil
}

// mapQuestions pairs statically declared identifiers. Generated code can emit
// their exact types and schemas instead of interpreting a catalog on each call.
func mapQuestions(mapping *Exchange) error {
	if mapping.Requests == nil && mapping.Responses == nil {
		return nil
	}
	if mapping.Requests == nil || mapping.Responses == nil {
		return fmt.Errorf("InputExchange requests and responses must declare the same identifiers")
	}
	requests, responses := expr.AsObject(mapping.Requests.Type), expr.AsObject(mapping.Responses.Type)
	if len(*requests) != len(*responses) {
		return fmt.Errorf("InputExchange requests and responses must declare the same identifiers")
	}
	for _, request := range *requests {
		response := responses.Attribute(request.Name)
		if request.Name == "" || response == nil || mapping.Requests.IsRequired(request.Name) || mapping.Responses.IsRequired(request.Name) {
			return fmt.Errorf("InputExchange question %q needs optional matching request and response fields", request.Name)
		}
		question, err := mapQuestion(request.Name, request.Attribute, response)
		if err != nil {
			return err
		}
		mapping.Questions = append(mapping.Questions, question)
	}
	return nil
}

// mapQuestion checks one form or URL request and its accept/decline/cancel
// response. Accepted form content is the only source for the advertised schema.
func mapQuestion(name string, request, response *expr.AttributeExpr) (*Question, error) {
	question := &Question{Name: name, Request: request, Response: response}
	fields := expr.AsObject(request.Type)
	if fields == nil || fields.Attribute("message") == nil || !request.IsRequired("message") || primitive(fields.Attribute("message").Type) != expr.String {
		return nil, fmt.Errorf("InputExchange request %q requires a message string", name)
	}
	url := fields.Attribute("url")
	if url == nil && len(*fields) != 1 || url != nil && (len(*fields) != 2 || !request.IsRequired("url") || primitive(url.Type) != expr.String) {
		return nil, fmt.Errorf("InputExchange request %q contains only required message and, for URL mode, required url", name)
	}
	if url != nil {
		validation := expr.EffectiveValidation(url)
		if validation == nil || validation.Format != expr.FormatURI {
			return nil, fmt.Errorf("InputExchange URL %q requires FormatURI", name)
		}
	}
	answers := expr.AsObject(response.Type)
	if answers == nil || len(*answers) != 1 || answers.Attribute("answer") == nil || !response.IsRequired("answer") {
		return nil, fmt.Errorf("InputExchange response %q requires only an answer OneOf", name)
	}
	question.Answer = answers.Attribute("answer")
	choice := expr.AsUnion(question.Answer.Type)
	if choice == nil || len(choice.Values) != 3 {
		return nil, fmt.Errorf("InputExchange response %q needs accept, decline and cancel branches", name)
	}
	var accepted, declined, canceled bool
	for _, branch := range choice.Values {
		body := expr.AsObject(branch.Attribute.Type)
		if body == nil {
			return nil, fmt.Errorf("InputExchange answer %q must be an object", branch.Name)
		}
		switch branch.Name {
		case "accept":
			accepted = true
			if url == nil {
				if len(*body) != 1 || body.Attribute("content") == nil || !branch.Attribute.IsRequired("content") {
					return nil, fmt.Errorf("InputExchange form %q acceptance requires only content", name)
				}
				question.Content = body.Attribute("content")
				if err := formContent(question.Content); err != nil {
					return nil, fmt.Errorf("InputExchange form %q: %w", name, err)
				}
			} else if len(*body) != 0 {
				return nil, fmt.Errorf("InputExchange URL %q acceptance cannot contain form data", name)
			}
		case "decline":
			declined = true
			if len(*body) != 0 {
				return nil, fmt.Errorf("InputExchange decline %q cannot contain form data", name)
			}
		case "cancel":
			canceled = true
			if len(*body) != 0 {
				return nil, fmt.Errorf("InputExchange cancel %q cannot contain form data", name)
			}
		default:
			return nil, fmt.Errorf("InputExchange answer has unknown action %q", branch.Name)
		}
	}
	if !accepted || !declined || !canceled {
		return nil, fmt.Errorf("InputExchange response %q needs accept, decline and cancel branches", name)
	}
	return question, nil
}

// formContent accepts the flat primitives and string-selection arrays allowed
// by MCP forms. Nested objects belong to ordinary tool arguments instead.
func formContent(attribute *expr.AttributeExpr) error {
	object := expr.AsObject(attribute.Type)
	if object == nil {
		return fmt.Errorf("content must be a flat object")
	}
	for _, field := range *object {
		switch primitive(field.Attribute.Type) {
		case expr.String, expr.Boolean, expr.Int, expr.Int32, expr.Int64, expr.UInt, expr.UInt32, expr.UInt64, expr.Float32, expr.Float64:
			continue
		}
		array := expr.AsArray(field.Attribute.Type)
		if array != nil && array.NonNullableElems && primitive(array.ElemType.Type) == expr.String {
			validation := expr.EffectiveValidation(array.ElemType)
			if validation != nil && len(validation.Values) > 0 {
				continue
			}
		}
		return fmt.Errorf("field %q must be a primitive or non-null string-selection array", field.Name)
	}
	return nil
}

// primitive unwraps authored aliases for shape checks while retaining the
// original attribute for native declarations, validation and generated names.
func primitive(value expr.DataType) expr.DataType {
	for {
		named, ok := value.(expr.UserType)
		if !ok {
			return value
		}
		value = named.Attribute().Type
	}
}

// restoreAuthoredFields returns the original declarations selected by validation.
// Generators can then resolve their existing names and package locations.
func (e *Exchange) restoreAuthoredFields(graph contractGraph) {
	e.Continuation = graph.original(e.Continuation)
	e.Responses = graph.original(e.Responses)
	e.Outcome = graph.original(e.Outcome)
	e.Complete = graph.original(e.Complete)
	e.Pending = graph.original(e.Pending)
	e.Requests = graph.original(e.Requests)
	for _, question := range e.Questions {
		question.restoreAuthoredFields(graph)
	}
}

// restoreAuthoredFields keeps each validated question tied to its service types.
func (q *Question) restoreAuthoredFields(graph contractGraph) {
	q.Request = graph.original(q.Request)
	q.Response = graph.original(q.Response)
	q.Answer = graph.original(q.Answer)
	q.Content = graph.original(q.Content)
}
