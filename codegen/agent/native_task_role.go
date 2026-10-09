// Package codegen prepares the existing Goa methods used to observe, answer and
// cancel a native job. Goa owns field copying and pointer representation; the
// generated final filler owns the saved job identity and payload validation.
package codegen

import (
	"fmt"
	"strings"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// nativeTaskRolePlan retains one existing method's payload conversion.
	nativeTaskRolePlan struct {
		method        *expr.MethodExpr
		prepare, fill *codegen.NameDeclaration
		validation    *jsoncodec.Value
		transform     *codegen.TransformPlan
		layout        *codegen.GoTypePlan
		data          nativeTaskRoleData
	}
	// nativeTaskRoleData writes the exact existing method input and its final filler.
	nativeTaskRoleData struct {
		Prepare, Fill, Validate                string
		PayloadRef, PayloadValueRef, SourceRef string
		TaskIDField, TaskIDRef, MethodName     string
		Body, Defaults                         string
		HasSource, Answer, ReturnsView         bool
		CallerField, CallerOption              string
	}
)

// planTaskRoles records all three operations once per creator. Shared fields
// come from the creator's retained payload; fields supplied by interceptors remain
// in the native payload and are validated after those interceptors have run.
func (p *toolSpecsPackagePlan) planTaskRoles(services *service.Plan, task *nativeTaskPlan) error {
	creator := task.binding.Creator
	for index, method := range []*expr.MethodExpr{task.binding.Read, task.binding.Answer, task.binding.Cancel} {
		role := &nativeTaskRolePlan{method: method}
		var err error
		role.layout, err = services.MethodTypeLayout(method, method.Payload)
		if err != nil {
			return err
		}
		prefix := codegen.Goify(creator.Name, true) + "Task" + codegen.Goify(method.Name, true)
		if method == task.binding.Answer {
			role.validation = task.answerValidation
			role.fill = task.fill
			role.data.Answer = true
		} else {
			role.validation, err = p.inputCodecs.Add(creator.Service.Name+":"+creator.Name+":task:role:"+method.Name, prefix+"Payload", method.Payload, role.layout, jsoncodec.ValidateOnly)
			if err != nil {
				return err
			}
			role.fill = codegen.NewPreferredName(codegen.NameFunction, "Fill"+prefix+"Input", codegen.ExportedName, specNameOrder{packagePath: p.public.ImportPath(), key: creator.Name + ":" + method.Name + ":fill"})
			if err := p.public.DeclareName(role.fill); err != nil {
				return err
			}
		}
		role.prepare = codegen.NewPreferredName(codegen.NameFunction, "To"+prefix+"Payload", codegen.ExportedName, specNameOrder{packagePath: p.public.ImportPath(), key: creator.Name + ":" + method.Name + ":prepare"})
		if err := p.public.DeclareName(role.prepare); err != nil {
			return err
		}
		source := expr.AsObject(creator.Payload.Type)
		for _, field := range *expr.AsObject(method.Payload.Type) {
			if field.Name == "taskId" || field.Name == "responses" || source == nil || source.Attribute(field.Name) == nil {
				continue
			}
			role.transform, err = p.declareTransform(creator.Name+":task:role:"+method.Name, creator.Payload, method.Payload, prefix)
			if err != nil {
				return err
			}
			layout, err := services.MethodTypeLayout(creator, creator.Payload)
			if err != nil {
				return err
			}
			p.setTransformLayouts(role.transform, layout, role.layout)
			break
		}
		task.roles[index] = role
	}
	return nil
}

// linkTaskRoles uses Goa's retained conversion plan instead of rebuilding field
// assignments. Each filler writes the workflow-owned identifier after application
// interceptors, then validates the complete native method input.
func (p *toolSpecsPackagePlan) linkTaskRoles(services *service.ServicesData, task *nativeTaskPlan) error {
	creator := task.binding.Creator
	codecTypes := services.ServiceAttributor(creator.Service.Name, p.inputCodecPackage.ImportPath())
	native := services.ServiceAttributor(creator.Service.Name, p.public.ImportPath())
	codecAlias := p.public.ImportName(p.inputCodecPackage.ImportPath())
	for _, role := range task.roles {
		if !role.data.Answer {
			if err := role.validation.BindService(codecTypes); err != nil {
				return err
			}
		}
		data := &role.data
		data.Prepare, data.Fill = role.prepare.Name(), role.fill.Name()
		data.Validate = codecAlias + "." + role.validation.ValidationDeclaration().Name()
		linked := role.layout.Link(p.public.ImportPath(), p.public.ImportName)
		data.PayloadRef, data.PayloadValueRef = linked.Ref(), linked.RefWithPointer(false)
		data.HasSource = creator.Payload.Type != expr.Empty
		if data.HasSource {
			data.SourceRef = native.Ref(creator.Payload, "")
		}
		identity := role.method.Payload.Find("taskId")
		fields := role.layout.PlansForOccurrence(identity)
		if len(fields) != 1 {
			return fmt.Errorf("TaskExchange method %q job identity has %d native layouts", role.method.Name, len(fields))
		}
		data.TaskIDField = fields[0].FieldName(true)
		data.TaskIDRef = fields[0].Link(p.public.ImportPath(), p.public.ImportName).RefWithPointer(false)
		method := services.Get(creator.Service.Name).Method(role.method.Name)
		data.MethodName = method.VarName
		data.ReturnsView = method.ViewedResult != nil && method.ViewedResult.ViewName == ""

		var defaults strings.Builder
		source := expr.AsObject(creator.Payload.Type)
		payload := expr.NewMappedAttributeExpr(role.method.Payload)
		for _, field := range *expr.AsObject(role.method.Payload.Type) {
			if field.Name == "taskId" || field.Name == "responses" || (source != nil && source.Attribute(field.Name) != nil) {
				continue
			}
			value := payload.GetDefault(field.Name)
			if value == nil {
				continue
			}
			layouts := role.layout.PlansForOccurrence(field.Attribute)
			if len(layouts) != 1 {
				return fmt.Errorf("TaskExchange method %q default field %q has %d layouts", role.method.Name, field.Name, len(layouts))
			}
			layout := layouts[0]
			code, err := codegen.RenderGoValue(field.Attribute, value, layout.Link(p.public.ImportPath(), p.public.ImportName), layout.IsPointer(), func(attribute *expr.AttributeExpr, branch string) (string, error) {
				var union *codegen.UnionDeclaration
				for _, candidate := range role.layout.PlansForOccurrence(attribute) {
					if candidate.UnionDeclaration() != nil {
						union = candidate.UnionDeclaration()
						break
					}
				}
				if union == nil {
					return "", fmt.Errorf("TaskExchange default union %q has no retained declaration", attribute.Type.Name())
				}
				owner := p.generation.Package(union.PackagePath())
				declaration, err := owner.UnionBranch(attribute, branch)
				if err != nil {
					return "", err
				}
				name := declaration.Constructor()
				if owner.ImportPath() != p.public.ImportPath() {
					name = p.public.ImportName(owner.ImportPath()) + "." + name
				}
				return name, nil
			}, field.Name+"Default")
			if err != nil {
				return err
			}
			for _, declaration := range code.Declarations {
				defaults.WriteString(declaration)
				defaults.WriteByte('\n')
			}
			fmt.Fprintf(&defaults, "out.%s = %s\n", layout.FieldName(true), code.Expression)
		}
		data.Defaults = defaults.String()
		if role.transform != nil {
			body, helpers, err := renderToolTransform(role.transform, serviceAttributeContext(native), serviceAttributeContext(native))
			if err != nil {
				return err
			}
			p.specs.adapterHelpers = codegen.AppendHelpers(p.specs.adapterHelpers, helpers)
			data.Body = body
		}
	}
	return nil
}
