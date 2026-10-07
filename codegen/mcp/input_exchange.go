// Package codegen connects authored continuation fields to MCP input rounds.
// Static question names, schemas and native types are resolved during generation;
// runtime code handles only the service's selected questions and host answers.
package codegen

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
	// inputExchangeAdapter retains exact native fields used by the input filler
	// and result converter. No continuation fields are advertised to a model.
	inputExchangeAdapter struct {
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
		Questions          []*inputQuestionAdapter
		mapping            *mcpinput.Exchange
		continuationLayout *codegen.GoTypePlan
		pendingLayout      *codegen.GoTypePlan
		pending            *expr.AttributeExpr
		validation         *jsoncodec.Value
	}
	// inputQuestionAdapter records one declared request and its answer decoder.
	inputQuestionAdapter struct {
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

// planInputExchangeCodecs records validators for native pending data and one
// answer codec per declared question. Forms derive the advertised schema and
// native field validation from the same answer, keeping both boundaries aligned.
func planInputExchangeCodecs(services *goaservice.Plan, prepared *preparedMCPService, data *AdapterData, codecs *jsoncodec.Plan, call *endpointMethodAdapter) error {
	mapping, err := mcpinput.InputExchange(call.method)
	if err != nil || mapping == nil {
		return err
	}
	input := &inputExchangeAdapter{mapping: mapping}
	call.InputExchange = input
	data.NeedsServerCodec = true
	input.continuationLayout, err = services.MethodTypeLayout(call.method, mapping.Continuation)
	if err != nil {
		return err
	}
	for _, branch := range expr.AsUnion(call.resultAttribute.Find(mapping.OutcomeName).Type).Values {
		if branch.Name == "input_required" {
			input.pending = expr.DupAtt(branch.Attribute)
			break
		}
	}
	if err := selectResultFields(input.pending, mapping.Pending, make(map[resultTypePair]expr.DataType)); err != nil {
		return err
	}
	input.pendingLayout, err = services.MethodTypeLayout(call.method, input.pending)
	if err != nil {
		return err
	}
	prefix := codegen.Goify(call.method.Name, true)
	input.validation, err = codecs.Add(prepared.userService.Name+":"+call.method.Name+":pending", prefix+"Pending", input.pending, input.pendingLayout, jsoncodec.ValidateOnly)
	if err != nil {
		return err
	}
	for index, question := range mapping.Questions {
		if err := checkContentGoType(question.Answer); err != nil {
			return fmt.Errorf("MCP question %q answer: %w", question.Name, err)
		}
		entry := &inputQuestionAdapter{Name: question.Name, question: question}
		if question.Content != nil {
			schema, err := jsonschema.BuildForm(prepared.root.API, question.Content, expr.MethodPayloadExampleIdentity(call.method).Member(mapping.ContinuationName).Member("responses").Member(question.Name).Member("answer").UnionMember("accept").Member("content"))
			if err != nil {
				return fmt.Errorf("MCP question %q: %w", question.Name, err)
			}
			entry.Schema = string(schema)
		}
		entry.responseLayout, err = services.MethodTypeLayout(call.method, question.Response)
		if err != nil {
			return err
		}
		answerLayout, err := services.MethodTypeLayout(call.method, question.Answer)
		if err != nil {
			return err
		}
		answerFields := entry.responseLayout.PlansForOccurrence(question.Answer)
		if len(answerFields) != 1 {
			return fmt.Errorf("MCP answer has %d native field layouts", len(answerFields))
		}
		entry.AnswerDereference = answerLayout.ReferenceIsPointer() && !answerFields[0].IsPointer()
		entry.answer, err = codecs.AddElicitation(prepared.userService.Name+":"+call.method.Name+":answer:"+question.Name, fmt.Sprintf("%sAnswer%d", prefix, index), question.Answer, answerLayout, question.Content != nil)
		if err != nil {
			return err
		}
		entry.request = input.pending.Find("requests").Find(question.Name)
		entry.requestLayout, err = services.MethodTypeLayout(call.method, entry.request)
		if err != nil {
			return err
		}
		input.Questions = append(input.Questions, entry)
	}
	return nil
}

// bindInputExchange connects saved native layouts and final codec names. The
// configured endpoint keeps its full outcome; completed conversions receive
// the selected branch without reconstructing omitted view fields.
func bindInputExchange(data *AdapterData, call *endpointMethodAdapter) error {
	input := call.InputExchange
	if input == nil {
		return nil
	}
	link := func(layout *codegen.GoTypePlan) codegen.LinkedGoType {
		return layout.Link(data.mcpImportPath, data.mcpPackage.ImportName)
	}
	mapping := input.mapping
	input.ContinuationField = codegen.GoifyAtt(mapping.Continuation, mapping.ContinuationName, true)
	input.ContinuationRef = link(input.continuationLayout).RefWithPointer(false)
	input.OutcomeValue = call.ResultValue + "." + codegen.GoifyAtt(call.resultAttribute.Find(mapping.OutcomeName), mapping.OutcomeName, true)
	input.PendingRef = link(input.pendingLayout).Ref()
	input.PendingValidate = data.CodecPackage + "." + input.validation.ValidationDeclaration().Name()
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
		entry.Decode = data.CodecPackage + "." + entry.answer.DecodeDeclaration().Name()
	}
	call.ResultValue = "completedResult"
	return nil
}
