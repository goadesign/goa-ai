// Package inputexchange retains the actual Goa method that owns each question and
// answer. Ordinary input rounds and asynchronous job observations use the same
// form schemas, native field layouts and exact host-answer decoders.
package inputexchange

import (
	"fmt"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa-ai/codegen/jsonschema"
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

// newQuestion plans one question from its producing method and one answer from
// its consuming method. The original attributes retain their generated names,
// field locations and pointer representation without copying native types.
func newQuestion(services *goaservice.Plan, api *expr.APIExpr, requestMethod, responseMethod *expr.MethodExpr, question *mcpinput.Question, request *expr.AttributeExpr, identity expr.ExampleIdentity, prefix string, index int, codecs *jsoncodec.Plan) (*Question, error) {
	if err := checkAnswerGoType(question.Answer); err != nil {
		return nil, fmt.Errorf("MCP question %q answer: %w", question.Name, err)
	}
	entry := &Question{Name: question.Name, question: question, request: request}
	if question.Content != nil {
		schema, err := jsonschema.BuildForm(api, question.Content, identity)
		if err != nil {
			return nil, fmt.Errorf("MCP question %q: %w", question.Name, err)
		}
		entry.Schema = string(schema)
	}
	var err error
	entry.responseLayout, err = services.MethodTypeLayout(responseMethod, question.Response)
	if err != nil {
		return nil, err
	}
	answerLayout, err := services.MethodTypeLayout(responseMethod, question.Answer)
	if err != nil {
		return nil, err
	}
	answerFields := entry.responseLayout.PlansForOccurrence(question.Answer)
	if len(answerFields) != 1 {
		return nil, fmt.Errorf("MCP answer has %d native field layouts", len(answerFields))
	}
	entry.AnswerDereference = answerLayout.ReferenceIsPointer() && !answerFields[0].IsPointer()
	entry.answer, err = codecs.AddElicitation(responseMethod.Service.Name+":"+responseMethod.Name+":"+prefix+":answer:"+question.Name, fmt.Sprintf("%sAnswer%d", prefix, index), question.Answer, answerLayout, question.Content != nil)
	if err != nil {
		return nil, err
	}
	entry.requestLayout, err = services.MethodTypeLayout(requestMethod, request)
	if err != nil {
		return nil, err
	}
	return entry, nil
}

// bindQuestion resolves the final native selectors and decoder name once Goa
// has assigned all declarations. Both input lifetimes use these same fields.
func bindQuestion(entry *Question, packagePath string, packageAlias codegen.GoTypeQualifier, codecAlias string) error {
	entry.ResponseField = codegen.GoifyAtt(entry.question.Response, entry.Name, true)
	entry.ResponseRef = entry.responseLayout.Link(packagePath, packageAlias).RefWithPointer(false)
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
	return nil
}
