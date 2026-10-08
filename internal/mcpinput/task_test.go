// These checks resolve native job operations and preserve their authored types.
// Invalid method roles and state shapes fail before generation can expose an
// incomplete job lifecycle or advertise its metadata as the finished result.
package mcpinput

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa/v3/expr"
)

func TestTaskExchangeKeepsNativeObservationAndDomainResult(t *testing.T) {
	creator := taskCreatorForTest()
	binding, err := TaskExchange(creator)
	require.NoError(t, err)
	assert.Same(t, creator, binding.Creator)
	assert.Same(t, creator.Service.Method("read"), binding.Read)
	assert.Same(t, creator.Service.Method("answer"), binding.Answer)
	assert.Same(t, creator.Service.Method("cancel"), binding.Cancel)
	assert.Same(t, creator.Result, binding.Observation)
	assert.Equal(t, expr.String, binding.Complete.Type)
	complete, err := CompleteResult(creator)
	require.NoError(t, err)
	assert.Same(t, binding.Complete, complete)
	require.Len(t, binding.Questions, 1)
	assert.Equal(t, "details", binding.Questions[0].Name)
	assert.Nil(t, creator.Result.Find("task").Find("status"))

	ordinary := *creator
	ordinary.Meta = nil
	binding, err = TaskExchange(&ordinary)
	require.NoError(t, err)
	assert.Nil(t, binding)
}

func TestTaskExchangeRejectsIncompleteNativeContracts(t *testing.T) {
	for _, tc := range []struct {
		name, failure string
		change        func(*expr.MethodExpr)
	}{
		{"missing read", "distinct method", func(m *expr.MethodExpr) { m.Meta[TaskExchangeMetaKey][0] = "absent" }},
		{"creator as read", "distinct method", func(m *expr.MethodExpr) { m.Meta[TaskExchangeMetaKey][0] = m.Name }},
		{"reused operation", "distinct method", func(m *expr.MethodExpr) { m.Meta[TaskExchangeMetaKey][2] = "answer" }},
		{"nested creator", "cannot create another job", func(m *expr.MethodExpr) {
			m.Service.Method("read").Meta = expr.MetaExpr{TaskExchangeMetaKey: {"read", "answer", "cancel"}}
		}},
		{"optional identity", "requires a taskId string", func(m *expr.MethodExpr) { m.Service.Method("read").Payload.Validation = nil }},
		{"numeric identity", "requires a taskId string", func(m *expr.MethodExpr) { m.Service.Method("read").Payload.Find("taskId").Type = expr.Int64 }},
		{"different observation", "same named observation", func(m *expr.MethodExpr) { m.Service.Method("read").Result = &expr.AttributeExpr{Type: expr.String} }},
		{"answer result", "no domain result", func(m *expr.MethodExpr) { m.Service.Method("answer").Result = &expr.AttributeExpr{Type: expr.String} }},
		{"optional metadata identity", "requires taskId", func(m *expr.MethodExpr) {
			m.Result.Find("task").Validation.Required = []string{"createdAt", "lastUpdatedAt"}
		}},
		{"second status", "unknown field", func(m *expr.MethodExpr) {
			fields := expr.AsObject(m.Result.Find("task").Type)
			*fields = append(*fields, &expr.NamedAttributeExpr{Name: "status", Attribute: &expr.AttributeExpr{Type: expr.String}})
		}},
		{"required retention", "optional Int64", func(m *expr.MethodExpr) {
			metadata := m.Result.Find("task")
			metadata.Validation.Required = append(metadata.Validation.Required, "ttlMs")
		}},
		{"narrow retention", "optional Int64", func(m *expr.MethodExpr) { m.Result.Find("task").Find("ttlMs").Type = expr.Int32 }},
		{"extra state", "requires working", func(m *expr.MethodExpr) {
			choice := expr.AsUnion(m.Result.Find("outcome").Type)
			choice.Values = append(choice.Values, choice.Values[0])
		}},
		{"repeated state", "repeats branch", func(m *expr.MethodExpr) {
			choice := expr.AsUnion(m.Result.Find("outcome").Type)
			choice.Values[4].Name = "working"
		}},
		{"working data", "empty object", func(m *expr.MethodExpr) {
			choice := expr.AsUnion(m.Result.Find("outcome").Type)
			choice.Values[0].Attribute.Type = expr.String
		}},
		{"missing input requests", "requires only a requests", func(m *expr.MethodExpr) { taskBranch(m, "input_required").Validation = nil }},
		{"untyped requests", "typed maps", func(m *expr.MethodExpr) { taskBranch(m, "input_required").Find("requests").Type = expr.Any }},
		{"optional responses", "requires a responses", func(m *expr.MethodExpr) { m.Service.Method("answer").Payload.Validation.Required = []string{"taskId"} }},
		{"missing failure code", "requires code", func(m *expr.MethodExpr) { taskBranch(m, "failed").Validation.Required = []string{"message"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creator := taskCreatorForTest()
			tc.change(creator)
			_, err := TaskExchange(creator)
			require.ErrorContains(t, err, tc.failure)
		})
	}
}

// taskCreatorForTest declares one typed job with its three ordinary Goa methods.
// All observations share the same named type and derive status from the union.
func taskCreatorForTest() *expr.MethodExpr {
	request := taskObjectForTest(&expr.Object{
		{Name: "message", Attribute: &expr.AttributeExpr{Type: expr.String}},
	}, "message")
	content := taskObjectForTest(&expr.Object{
		{Name: "name", Attribute: &expr.AttributeExpr{Type: expr.String}},
	}, "name")
	answer := &expr.AttributeExpr{Type: &expr.Union{
		TypeName: "DetailsAnswer",
		Values: []*expr.NamedAttributeExpr{
			{Name: "accept", Attribute: taskObjectForTest(&expr.Object{{Name: "content", Attribute: content}}, "content")},
			{Name: "decline", Attribute: taskObjectForTest(&expr.Object{})},
			{Name: "cancel", Attribute: taskObjectForTest(&expr.Object{})},
		},
	}}
	response := taskObjectForTest(&expr.Object{{Name: "answer", Attribute: answer}}, "answer")
	requestKind := &expr.AttributeExpr{Type: &expr.Union{
		TypeName: "JobRequest",
		Values:   []*expr.NamedAttributeExpr{{Name: "details", Attribute: request}},
	}}
	responseKind := &expr.AttributeExpr{Type: &expr.Union{
		TypeName: "JobResponse",
		Values:   []*expr.NamedAttributeExpr{{Name: "details", Attribute: response}},
	}}
	requests := &expr.AttributeExpr{Type: &expr.Map{
		KeyType:  &expr.AttributeExpr{Type: expr.String},
		ElemType: taskObjectForTest(&expr.Object{{Name: "request", Attribute: requestKind}}, "request"),
	}}
	responses := &expr.AttributeExpr{Type: &expr.Map{
		KeyType:  &expr.AttributeExpr{Type: expr.String},
		ElemType: taskObjectForTest(&expr.Object{{Name: "response", Attribute: responseKind}}, "response"),
	}}
	metadata := taskObjectForTest(&expr.Object{
		{Name: "taskId", Attribute: &expr.AttributeExpr{Type: expr.String}},
		{Name: "createdAt", Attribute: &expr.AttributeExpr{Type: expr.String}},
		{Name: "lastUpdatedAt", Attribute: &expr.AttributeExpr{Type: expr.String}},
		{Name: "ttlMs", Attribute: &expr.AttributeExpr{Type: expr.Int64}},
	}, "taskId", "createdAt", "lastUpdatedAt")
	observation := &expr.AttributeExpr{Type: &expr.UserTypeExpr{
		TypeName: "JobObservation", UID: "job-observation",
		AttributeExpr: taskObjectForTest(&expr.Object{
			{Name: "task", Attribute: metadata},
			{Name: "outcome", Attribute: &expr.AttributeExpr{Type: &expr.Union{TypeName: "JobOutcome", Values: []*expr.NamedAttributeExpr{
				{Name: "working", Attribute: taskObjectForTest(&expr.Object{})},
				{Name: "input_required", Attribute: taskObjectForTest(&expr.Object{{Name: "requests", Attribute: requests}}, "requests")},
				{Name: "complete", Attribute: &expr.AttributeExpr{Type: expr.String}},
				{Name: "failed", Attribute: taskObjectForTest(&expr.Object{
					{Name: "code", Attribute: &expr.AttributeExpr{Type: expr.Int}},
					{Name: "message", Attribute: &expr.AttributeExpr{Type: expr.String}},
				}, "code", "message")},
				{Name: "cancelled", Attribute: taskObjectForTest(&expr.Object{})},
			}}}},
		}, "task", "outcome"),
	}}
	service := &expr.ServiceExpr{Name: "reports"}
	creator := &expr.MethodExpr{Name: "create", Service: service, Payload: &expr.AttributeExpr{Type: expr.Empty}, Result: observation, Meta: expr.MetaExpr{TaskExchangeMetaKey: {"read", "answer", "cancel"}}}
	service.Methods = append(service.Methods, creator)
	for _, name := range []string{"read", "answer", "cancel"} {
		payload := taskObjectForTest(&expr.Object{{Name: "taskId", Attribute: &expr.AttributeExpr{Type: expr.String}}}, "taskId")
		result := &expr.AttributeExpr{Type: expr.Empty}
		if name == "read" {
			result = observation
		}
		if name == "answer" {
			fields := expr.AsObject(payload.Type)
			*fields = append(*fields, &expr.NamedAttributeExpr{Name: "responses", Attribute: responses})
			payload.Validation.Required = append(payload.Validation.Required, "responses")
		}
		service.Methods = append(service.Methods, &expr.MethodExpr{Name: name, Service: service, Payload: payload, Result: result})
	}
	return creator
}

// taskObjectForTest creates fixture fields with explicit presence constraints.
func taskObjectForTest(object *expr.Object, required ...string) *expr.AttributeExpr {
	return &expr.AttributeExpr{Type: object, Validation: &expr.ValidationExpr{Required: required}}
}

// taskBranch selects the fixture branch that one boundary case will invalidate.
func taskBranch(method *expr.MethodExpr, name string) *expr.AttributeExpr {
	for _, branch := range expr.AsUnion(method.Result.Find("outcome").Type).Values {
		if branch.Name == name {
			return branch.Attribute
		}
	}
	panic("fixture task branch missing")
}
