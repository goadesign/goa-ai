// Package inputexchange resolves questions produced by a job read and answers consumed
// by its separate update method. The service owns the dynamic question keys;
// generated code retains the authored map, union and answer types.
package inputexchange

import (
	"encoding/base64"
	"fmt"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa-ai/internal/mcpinput"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// TaskPlan connects one native job's questions to its typed answer map.
	TaskPlan struct {
		// Native and MCPPackage select the emitted host-request representation.
		Native     bool
		MCPPackage string
		// Questions uses the same form and answer plans as ordinary input rounds.
		Questions []*TaskQuestion
		// PendingRef and PendingValidate name the native input state and its validator.
		PendingRef, PendingValidate string
		// RequestsField and RequestField locate the returned map and each request union.
		RequestsField, RequestField string
		// ResponsesField and ResponseField locate the submitted map and each response union.
		ResponsesField, ResponseField string
		// ResponsesRef and ResponseRef name the native map and its element object.
		ResponsesRef, ResponseRef string
		// ResponseKeyRef retains the authored map key's native Go type.
		ResponseKeyRef                          string
		binding                                 *mcpinput.TaskBinding
		pending                                 *expr.AttributeExpr
		pendingLayout, responseMapLayout        *codegen.GoTypePlan
		requestUnionLayout, responseUnionLayout *codegen.GoTypePlan
		validation                              *jsoncodec.Value
	}

	// TaskQuestion retains one statically declared kind inside a dynamic job map.
	TaskQuestion struct {
		// Question retains the shared native fields and answer decoder.
		*Question
		// KeyKind encodes the declared question kind inside its opaque host key.
		KeyKind string
		// RequestKind is Goa's discriminator for this native question branch.
		RequestKind string
		// RequestAccessor reads this kind from the native request union.
		RequestAccessor string
		// ResponseConstructor constructs this kind in the native response union.
		ResponseConstructor string
		// ResponsePointer retains Goa's pointer representation for the union field.
		ResponsePointer bool
	}
)

// NewTask plans host answers against the update method's actual fields, while
// validating questions against the read method's actual result. No synthetic
// method or copied type is needed to connect those two owners.
func NewTask(services *goaservice.Plan, api *expr.APIExpr, binding *mcpinput.TaskBinding, pending *expr.AttributeExpr, codecs *jsoncodec.Plan) (*TaskPlan, error) {
	plan := &TaskPlan{binding: binding, pending: pending}
	var err error
	plan.pendingLayout, err = services.MethodTypeLayout(binding.Read, pending)
	if err != nil {
		return nil, err
	}
	prefix := codegen.Goify(binding.Creator.Name, true) + "Task"
	plan.validation, err = codecs.Add(binding.Creator.Service.Name+":"+binding.Creator.Name+":task:pending", prefix+"Pending", pending, plan.pendingLayout, jsoncodec.ValidateOnly)
	if err != nil {
		return nil, err
	}
	responses := binding.Answer.Payload.Find("responses")
	plan.responseMapLayout, err = services.MethodTypeLayout(binding.Answer, responses)
	if err != nil {
		return nil, err
	}
	responseUnion := expr.AsMap(responses.Type).ElemType.Find("response")
	plan.responseUnionLayout, err = services.MethodTypeLayout(binding.Answer, responseUnion)
	if err != nil {
		return nil, err
	}
	requestUnion := expr.AsMap(pending.Find("requests").Type).ElemType.Find("request")
	requestLayouts := plan.pendingLayout.PlansForOccurrence(requestUnion)
	if len(requestLayouts) != 1 {
		return nil, fmt.Errorf("TaskExchange request union has %d returned field layouts", len(requestLayouts))
	}
	plan.requestUnionLayout = requestLayouts[0]
	for index, question := range binding.Questions {
		identity := expr.MethodPayloadExampleIdentity(binding.Answer).Member("responses").MapValue(0).Member("response").UnionMember(question.Name).Member("answer").UnionMember("accept").Member("content")
		var request *expr.AttributeExpr
		for _, branch := range expr.AsUnion(requestUnion.Type).Values {
			if branch.Name == question.Name {
				request = branch.Attribute
				break
			}
		}
		entry, err := newQuestion(services, api, binding.Read, binding.Answer, question, request, identity, prefix, index, codecs)
		if err != nil {
			return nil, err
		}
		plan.Questions = append(plan.Questions, &TaskQuestion{Question: entry, KeyKind: base64.RawURLEncoding.EncodeToString([]byte(question.Name)), RequestAccessor: "As" + codegen.Goify(question.Name, true)})
	}
	return plan, nil
}

// BindCodecs keeps the read view and native answer method under their existing
// generated type owners. The pending validator accepts the exact returned view.
func (plan *TaskPlan) BindCodecs(native, pending codegen.Attributor) error {
	if err := plan.validation.BindService(pending); err != nil {
		return err
	}
	for _, question := range plan.Questions {
		if err := question.answer.BindService(native); err != nil {
			return err
		}
	}
	return nil
}

// Bind resolves final selectors and constructors from Goa's retained layouts.
// The resulting code fills the exact update map without changing its union or
// rebuilding the question schema during execution.
func (plan *TaskPlan) Bind(generation *codegen.Generation, native codegen.Attributor, packagePath string, packageAlias codegen.GoTypeQualifier, codecAlias string) error {
	plan.PendingRef = plan.pendingLayout.Link(packagePath, packageAlias).Ref()
	plan.PendingValidate = codecAlias + "." + plan.validation.ValidationDeclaration().Name()
	requests := plan.pending.Find("requests")
	requestUnion := expr.AsMap(requests.Type).ElemType.Find("request")
	plan.RequestsField = native.Field(requests, "requests", true)
	plan.RequestField = native.Field(requestUnion, "request", true)
	responses := plan.binding.Answer.Payload.Find("responses")
	responseUnion := expr.AsMap(responses.Type).ElemType.Find("response")
	plan.ResponsesField = native.Field(responses, "responses", true)
	plan.ResponseField = native.Field(responseUnion, "response", true)
	plan.ResponsesRef = plan.responseMapLayout.Link(packagePath, packageAlias).Ref()
	plan.ResponseRef = plan.responseMapLayout.Elem().Link(packagePath, packageAlias).RefWithPointer(false)
	plan.ResponseKeyRef = plan.responseMapLayout.Key().Link(packagePath, packageAlias).Ref()
	occurrences := plan.responseMapLayout.PlansForOccurrence(responseUnion)
	if len(occurrences) != 1 {
		return fmt.Errorf("TaskExchange response union has %d native field layouts", len(occurrences))
	}
	for _, question := range plan.Questions {
		if err := bindQuestion(question.Question, packagePath, packageAlias, codecAlias); err != nil {
			return err
		}
		requestOwner := generation.Package(plan.requestUnionLayout.UnionDeclaration().PackagePath())
		requestBranch, err := requestOwner.UnionBranch(requestUnion, question.Name)
		if err != nil {
			return err
		}
		question.RequestKind = requestBranch.KindConst()
		if requestOwner.ImportPath() != packagePath {
			question.RequestKind = packageAlias(requestOwner.ImportPath()) + "." + question.RequestKind
		}
		owner := generation.Package(plan.responseUnionLayout.UnionDeclaration().PackagePath())
		branch, err := owner.UnionBranch(responseUnion, question.Name)
		if err != nil {
			return err
		}
		question.ResponseConstructor = branch.Constructor()
		if owner.ImportPath() != packagePath {
			question.ResponseConstructor = packageAlias(owner.ImportPath()) + "." + question.ResponseConstructor
		}
		question.ResponsePointer = occurrences[0].IsPointer()
	}
	return nil
}
