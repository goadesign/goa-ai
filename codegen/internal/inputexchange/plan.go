// Package inputexchange connects authored continuation fields to MCP input rounds.
// Static question names, schemas and native types are resolved during generation;
// runtime code handles only the service's selected questions and host answers.
package inputexchange

import (
	"fmt"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa-ai/codegen/internal/jsonschema"
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// Plan retains exact native fields used by the input filler
	// and result converter. No continuation fields are advertised to a model.
	Plan struct {
		// ContinuationField and ContinuationRef locate the native payload's input round.
		ContinuationField, ContinuationRef string
		// StateField and StateRef retain the authored state field's selector and type.
		StateField, StateRef string
		// ResponsesField and ResponsesRef locate the native answer object.
		ResponsesField, ResponsesRef string
		// OutcomeValue selects the endpoint's full union; PendingRef names its input branch.
		OutcomeValue, PendingRef string
		// PendingStateField and RequestsField locate returned state and selected questions.
		PendingStateField, RequestsField string
		// PendingValidate names the generated validator for returned questions.
		PendingValidate string
		// Questions lists the statically declared question and answer pairs.
		Questions []*Question
		// Native emits the workflow runtime result rather than the MCP wire result.
		Native bool
		// MCPPackage names the runtime package used by native generated conversions.
		MCPPackage         string
		mapping            *mcpinput.Exchange
		continuationLayout *codegen.GoTypePlan
		pendingLayout      *codegen.GoTypePlan
		pending            *expr.AttributeExpr
		validation         *jsoncodec.Value
	}
	// Question records one declared request and its answer decoder.
	Question struct {
		// Name, RequestField and ResponseField correlate one authored question and answer.
		Name, RequestField, ResponseField string
		// ResponseRef and AnswerField locate the native answer union.
		ResponseRef, AnswerField string
		// MessageField, URLField and Schema supply the question shown to the host.
		MessageField, URLField, Schema string
		// MessagePointer and URLPointer retain Goa's selected-view field representation.
		MessagePointer, URLPointer bool
		// AnswerDereference copies a decoded union pointer into a native value field.
		AnswerDereference bool
		// Decode names the generated decoder for this question's host answer.
		Decode         string
		question       *mcpinput.Question
		request        *expr.AttributeExpr
		responseLayout *codegen.GoTypePlan
		requestLayout  *codegen.GoTypePlan
		answer         *jsoncodec.Value
	}
)

// New plans typed host-answer decoders and the pending-result validator for one
// method. The caller supplies the actual native or selected-view pending type.
func New(services *goaservice.Plan, api *expr.APIExpr, method *expr.MethodExpr, pending *expr.AttributeExpr, codecs *jsoncodec.Plan) (*Plan, error) {
	mapping, err := mcpinput.InputExchange(method)
	if err != nil || mapping == nil {
		return nil, err
	}
	input := &Plan{mapping: mapping, pending: pending}
	input.continuationLayout, err = services.MethodTypeLayout(method, mapping.Continuation)
	if err != nil {
		return nil, err
	}
	input.pendingLayout, err = services.MethodTypeLayout(method, input.pending)
	if err != nil {
		return nil, err
	}
	prefix := codegen.Goify(method.Name, true)
	input.validation, err = codecs.Add(method.Service.Name+":"+method.Name+":pending", prefix+"Pending", input.pending, input.pendingLayout, jsoncodec.ValidateOnly)
	if err != nil {
		return nil, err
	}
	for index, question := range mapping.Questions {
		if err := checkAnswerGoType(question.Answer); err != nil {
			return nil, fmt.Errorf("MCP question %q answer: %w", question.Name, err)
		}
		entry := &Question{Name: question.Name, question: question}
		if question.Content != nil {
			schema, err := jsonschema.BuildForm(api, question.Content, expr.MethodPayloadExampleIdentity(method).Member(mapping.ContinuationName).Member("responses").Member(question.Name).Member("answer").UnionMember("accept").Member("content"))
			if err != nil {
				return nil, fmt.Errorf("MCP question %q: %w", question.Name, err)
			}
			entry.Schema = string(schema)
		}
		entry.responseLayout, err = services.MethodTypeLayout(method, question.Response)
		if err != nil {
			return nil, err
		}
		answerLayout, err := services.MethodTypeLayout(method, question.Answer)
		if err != nil {
			return nil, err
		}
		answerFields := entry.responseLayout.PlansForOccurrence(question.Answer)
		if len(answerFields) != 1 {
			return nil, fmt.Errorf("MCP answer has %d native field layouts", len(answerFields))
		}
		entry.AnswerDereference = answerLayout.ReferenceIsPointer() && !answerFields[0].IsPointer()
		entry.answer, err = codecs.AddElicitation(method.Service.Name+":"+method.Name+":answer:"+question.Name, fmt.Sprintf("%sAnswer%d", prefix, index), question.Answer, answerLayout, question.Content != nil)
		if err != nil {
			return nil, err
		}
		entry.request = input.pending.Find("requests").Find(question.Name)
		entry.requestLayout, err = services.MethodTypeLayout(method, entry.request)
		if err != nil {
			return nil, err
		}
		input.Questions = append(input.Questions, entry)
	}
	return input, nil
}

// Bind links final native selectors and codec names in the package containing
// the generated conversion. The outcome expression names the caller's result.
func (input *Plan) Bind(packagePath string, packageAlias codegen.GoTypeQualifier, codecAlias, outcome string) error {
	link := func(layout *codegen.GoTypePlan) codegen.LinkedGoType {
		return layout.Link(packagePath, packageAlias)
	}
	mapping := input.mapping
	input.ContinuationField = codegen.GoifyAtt(mapping.Continuation, mapping.ContinuationName, true)
	input.ContinuationRef = link(input.continuationLayout).RefWithPointer(false)
	input.OutcomeValue = outcome
	input.PendingRef = link(input.pendingLayout).Ref()
	input.PendingValidate = codecAlias + "." + input.validation.ValidationDeclaration().Name()
	if state := mapping.Continuation.Find("state"); state != nil {
		matches := input.continuationLayout.PlansForOccurrence(state)
		if len(matches) != 1 {
			return fmt.Errorf("MCP continuation state has %d native layouts", len(matches))
		}
		input.StateField = matches[0].FieldName(true)
		input.StateRef = link(matches[0]).RefWithPointer(false)
		input.PendingStateField = codegen.GoifyAtt(input.pending.Find("state"), "state", true)
	}
	if mapping.Responses != nil {
		input.ResponsesField = codegen.GoifyAtt(mapping.Responses, "responses", true)
		matches := input.continuationLayout.PlansForOccurrence(mapping.Responses)
		if len(matches) != 1 {
			return fmt.Errorf("MCP continuation responses have %d native layouts", len(matches))
		}
		input.ResponsesRef = link(matches[0]).RefWithPointer(false)
		input.RequestsField = codegen.GoifyAtt(input.pending.Find("requests"), "requests", true)
	}
	for _, entry := range input.Questions {
		entry.ResponseField = codegen.GoifyAtt(entry.question.Response, entry.Name, true)
		entry.ResponseRef = link(entry.responseLayout).RefWithPointer(false)
		entry.AnswerField = codegen.GoifyAtt(entry.question.Answer, "answer", true)
		entry.RequestField = codegen.GoifyAtt(entry.request, entry.Name, true)
		entry.MessageField = codegen.GoifyAtt(entry.request.Find("message"), "message", true)
		message := entry.requestLayout.PlansForOccurrence(entry.request.Find("message"))
		if len(message) != 1 {
			return fmt.Errorf("MCP request message has %d native layouts", len(message))
		}
		entry.MessagePointer = message[0].IsPointer()
		if url := entry.request.Find("url"); url != nil {
			entry.URLField = codegen.GoifyAtt(url, "url", true)
			urlLayout := entry.requestLayout.PlansForOccurrence(url)
			if len(urlLayout) != 1 {
				return fmt.Errorf("MCP request URL has %d native layouts", len(urlLayout))
			}
			entry.URLPointer = urlLayout[0].IsPointer()
		}
		entry.Decode = codecAlias + "." + entry.answer.DecodeDeclaration().Name()
	}
	return nil
}

// BindCodecs connects answer decoders to native service types and pending
// validation to the native or selected-view type supplied during planning.
func (input *Plan) BindCodecs(native, pending codegen.Attributor) error {
	if err := input.validation.BindService(pending); err != nil {
		return err
	}
	for _, question := range input.Questions {
		if err := question.answer.BindService(native); err != nil {
			return err
		}
	}
	return nil
}

// checkAnswerGoType rejects replaced Go fields whose authored form schema no
// longer describes the value the native method receives.
func checkAnswerGoType(attribute *expr.AttributeExpr) error {
	return codegen.Walk(attribute, func(current *expr.AttributeExpr) error {
		if len(current.Meta["struct:field:type"]) > 0 {
			return fmt.Errorf("struct:field:type is unsupported; use a Goa type with its declared fields")
		}
		return nil
	})
}
