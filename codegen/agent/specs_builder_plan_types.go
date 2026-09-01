// Package codegen turns Goa and Goa-AI designs into generated agent code.
//
// This file connects planned packages to Goa service data and records the
// generated types and value conversions used by tool and completion packages.
package codegen

import (
	"fmt"
	"path"
	"slices"

	"goa.design/goa-ai/expr/agent"
	goacodegen "goa.design/goa/v3/codegen"
	goaexpr "goa.design/goa/v3/expr"
)

// packageFor returns the saved public and HTTP package settings for dir.
func (p *toolSpecsPlan) packageFor(dir string) (*toolSpecsPackagePlan, error) {
	planned := p.byDir[dir]
	if planned == nil {
		return nil, fmt.Errorf("tool specs package %q was not planned", dir)
	}
	return planned, nil
}

// link builds each tool package once after Goa has built the service types.
// Every reference to the same output directory reads the same saved data.
func (p *toolSpecsPlan) link(data *GeneratorData) error {
	services := p.service.Services()
	dirs := make([]string, 0, len(p.byDir))
	for dir := range p.byDir {
		dirs = append(dirs, dir)
	}
	slices.Sort(dirs)
	for _, dir := range dirs {
		planned := p.byDir[dir]
		if err := planned.fileImports.link(); err != nil {
			return fmt.Errorf("link tool specs package %q imports: %w", dir, err)
		}
		owner, err := newDefinitionToolsetData(planned.definition, services, p.mcp)
		if err != nil {
			return err
		}
		if planned.registry {
			planned.render = owner
			continue
		}
		if err := linkToolsetNames(planned, owner); err != nil {
			return err
		}
		if err := p.linkMethodToolset(planned, owner, services); err != nil {
			return err
		}
		planned.specs, err = buildToolSpecsDataForPackage(
			data.Genpkg,
			owner.SourceService,
			owner.Tools,
			planned,
			p.api,
		)
		if err != nil {
			return err
		}
		if toolsetHasMethodTools(owner) {
			planned.specs.providerImports = importsForPaths(planned.public, planned.providerImportPaths)
			planned.specs.serviceTypeRef = planned.public.ImportName(planned.serviceImportPath) + "." + owner.SourceService.ServiceDeclaration.Name()
		}
		if err := planned.linkToolTransforms(owner, services); err != nil {
			return err
		}
		owner.specs = planned.specs
		planned.render = owner
	}

	for _, service := range data.Services {
		for _, agent := range service.Agents {
			for _, toolset := range agent.AllToolsets {
				if toolset == nil || len(toolset.Tools) == 0 {
					continue
				}
				planned, err := p.packageFor(toolset.SpecsDir)
				if err != nil {
					return err
				}
				if err := linkToolsetNames(planned, toolset); err != nil {
					return err
				}
				if err := p.linkMethodToolset(planned, toolset, services); err != nil {
					return err
				}
				toolset.specs = planned.specs
			}
		}
		if len(service.Completions) == 0 {
			continue
		}
		planned := p.completions[service.Service.Name]
		if planned == nil {
			return fmt.Errorf("service %q completion package was not planned", service.Service.Name)
		}
		if err := planned.fileImports.link(); err != nil {
			return fmt.Errorf("link service %q completion imports: %w", service.Service.Name, err)
		}
		var err error
		planned.completion, err = buildCompletionSpecsDataForPackage(
			data.Genpkg,
			service.Service,
			service.Completions,
			planned,
			p.api,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// declareToolTypeImports records every package needed by the input, result,
// and server data before generated declarations request names in the same Go
// package.
func (p *toolSpecsPackagePlan) declareToolTypeImports(toolset string, tool *agent.ToolExpr) error {
	owner := &contractTypeOwner{
		Kind:                     contractTypeOwnerTool,
		Name:                     tool.Name,
		QualifiedName:            toolset + "." + tool.Name,
		ScopeName:                toolset,
		Bounds:                   boundsData(tool.Bounds, tool.Method),
		ModelHiddenPayloadFields: slices.Clone(tool.InjectedFields),
	}
	payload := tool.Args
	if payload == nil || payload.Type == nil || payload.Type == goaexpr.Empty {
		payload = &goaexpr.AttributeExpr{Type: &goaexpr.Object{}}
	}
	if err := p.declareTypeImports(owner, payload, usagePayload); err != nil {
		return err
	}
	result := tool.Return
	if (result == nil || result.Type == nil || result.Type == goaexpr.Empty) && tool.Method != nil {
		result = tool.Method.Result
	}
	if result == nil {
		result = &goaexpr.AttributeExpr{Type: goaexpr.Empty}
	}
	if err := p.declareTypeImports(owner, result, usageResult); err != nil {
		return err
	}
	for _, serverData := range tool.ServerData {
		if serverData == nil || serverData.Schema == nil {
			continue
		}
		if err := p.declareTypeImports(owner, serverData.Schema, usageServerData); err != nil {
			return err
		}
	}
	return nil
}

// declareToolTypes records the input, result, and server-data types generated
// for tool.
func (p *toolSpecsPackagePlan) declareToolTypes(toolset string, tool *agent.ToolExpr) error {
	owner := &contractTypeOwner{
		Kind:                     contractTypeOwnerTool,
		Name:                     tool.Name,
		QualifiedName:            toolset + "." + tool.Name,
		ScopeName:                toolset,
		Bounds:                   boundsData(tool.Bounds, tool.Method),
		ModelHiddenPayloadFields: slices.Clone(tool.InjectedFields),
	}
	payload := tool.Args
	if payload == nil || payload.Type == nil || payload.Type == goaexpr.Empty {
		payload = &goaexpr.AttributeExpr{Type: &goaexpr.Object{}}
	}
	if err := p.declareType(owner, payload, usagePayload, ""); err != nil {
		return err
	}
	names := p.tools[tool.Name]
	names.payloadType = p.types[stableTypeKey(owner, usagePayload, "")]
	names.payloadType.jsonValidator.render = true
	publicPayload := effectiveObject(names.payloadType.publicShape)
	for _, name := range tool.InjectedFields {
		field := publicPayload.Attribute(name)
		if field == nil {
			return fmt.Errorf("plan injected field %q for tool %q: field is missing", name, tool.Name)
		}
		layout, err := goacodegen.PlanGoType(field, goacodegen.GoTypePlanOptions{
			Owner:     p.public.ImportPath(),
			FieldName: name,
			Policy: goacodegen.GoLayoutPolicy{
				UseDefault: true,
				SumType:    true,
			},
		})
		if err != nil {
			return fmt.Errorf("plan injected field %q for tool %q: %w", name, tool.Name, err)
		}
		names.injectedFieldLayouts[name] = layout
	}
	result := tool.Return
	if (result == nil || result.Type == nil || result.Type == goaexpr.Empty) && tool.Method != nil {
		result = tool.Method.Result
	}
	if result == nil {
		result = &goaexpr.AttributeExpr{Type: goaexpr.Empty}
	}
	if err := p.declareType(owner, result, usageResult, ""); err != nil {
		return err
	}
	names.resultType = p.types[stableTypeKey(owner, usageResult, "")]
	if result.Type != goaexpr.Empty {
		names.resultType.jsonValidator.render = true
	}
	for _, serverData := range tool.ServerData {
		if serverData == nil || serverData.Schema == nil {
			continue
		}
		if err := p.declareType(owner, serverData.Schema, usageServerData, serverData.Kind); err != nil {
			return err
		}
		names.serverDataTypes[serverData.Kind] = p.types[stableTypeKey(owner, usageServerData, serverData.Kind)]
		names.serverDataTypes[serverData.Kind].jsonValidator.render = true
	}
	return nil
}

// declareToolTransforms records every conversion used by a method-backed tool.
func (p *toolSpecsPackagePlan) declareToolTransforms(toolset string, tool *agent.ToolExpr) error {
	if tool.Method == nil {
		return nil
	}
	qualified := toolset + "." + tool.Name
	names := p.tools[tool.Name]
	owner := &contractTypeOwner{
		Kind:          contractTypeOwnerTool,
		Name:          tool.Name,
		QualifiedName: qualified,
		ScopeName:     toolset,
	}
	payload := p.types[stableTypeKey(owner, usagePayload, "")]
	if payload != nil && tool.Method.Payload != nil && tool.Method.Payload.Type != goaexpr.Empty {
		if err := goacodegen.IsCompatible(payload.public.Type, tool.Method.Payload.Type, "in", "out"); err == nil {
			planned, err := p.declareAdapterTransform(qualified+":method-payload", payload.public, tool.Method.Payload)
			if err != nil {
				return err
			}
			names.methodPayloadTransformPlan = planned
		}
	}
	result := p.types[stableTypeKey(owner, usageResult, "")]
	if result != nil && tool.Method.Result != nil && tool.Method.Result.Type != goaexpr.Empty {
		if err := goacodegen.IsCompatible(tool.Method.Result.Type, result.public.Type, "in", "out"); err == nil {
			planned, err := p.declareAdapterTransform(qualified+":tool-result", tool.Method.Result, result.public)
			if err != nil {
				return err
			}
			names.toolResultTransformPlan = planned
		}
	}
	for _, serverData := range tool.ServerData {
		if serverData.Source == nil || serverData.Source.MethodResultField == "" {
			continue
		}
		source := tool.Method.Result.Find(serverData.Source.MethodResultField)
		target := p.types[stableTypeKey(owner, usageServerData, serverData.Kind)]
		if source == nil || target == nil {
			return fmt.Errorf("server data kind %q has no source or output type", serverData.Kind)
		}
		if err := goacodegen.IsCompatible(source.Type, target.public.Type, "in", "out"); err != nil {
			return fmt.Errorf("server data kind %q: %w", serverData.Kind, err)
		}
		planned, err := p.declareAdapterTransform(qualified+":server-data:"+serverData.Kind, source, target.public)
		if err != nil {
			return err
		}
		names.serverDataTransformPlans[serverData.Kind] = planned
	}
	return nil
}

// declareType records one public Go type, its JSON-decoding Go type, every
// union it contains, and the functions that copy values between the two types.
func (p *toolSpecsPackagePlan) declareType(owner *contractTypeOwner, attribute *goaexpr.AttributeExpr, usage typeUsage, qualifier string) error {
	identity := stableTypeKey(owner, usage, qualifier)
	if p.types[identity] != nil {
		return nil
	}
	key := identity.orderKey()
	preferred := goacodegen.Goify(owner.Name, true)
	switch usage {
	case usagePayload:
		preferred += "Payload"
	case usageResult:
		preferred += "Result"
	case usageServerData:
		preferred += goacodegen.Goify(qualifier, true) + "ServerData"
	}
	shapes := localizedSpecShapes(owner, attribute, usage)
	public := shapes.public
	publicTypes := shapes.publicTypes
	if err := p.declareLocalTypes(p.public, p.publicTypes, p.publicTypeUses, publicTypes); err != nil {
		return err
	}
	publicType := &goaexpr.UserTypeExpr{
		AttributeExpr: stripStructPkgMeta(public),
		TypeName:      preferred,
	}
	publicAttribute := &goaexpr.AttributeExpr{Type: publicType}
	publicTypeDeclaration, err := p.public.DeclareGeneratedType(
		preferred,
		specNameOrder{packagePath: p.public.ImportPath(), key: key + ":public"},
	)
	if err != nil {
		return err
	}
	if err := p.public.BindGeneratedType(publicType, publicTypeDeclaration); err != nil {
		return err
	}
	publicDeclaration := publicTypeDeclaration.Declaration()
	p.publicTypeUses[publicType] = publicDeclaration
	if err := declareAttributeUnions(p.public, p.publicFixed, p.publicUnionErrors, public); err != nil {
		return err
	}

	publicLayout, err := p.planDeclaredTypeLayout(
		publicAttribute,
		p.public,
		goacodegen.GoLayoutPolicy{UseDefault: true, SumType: true},
	)
	if err != nil {
		return err
	}

	var (
		transportAttribute   *goaexpr.AttributeExpr
		transportDeclaration *goacodegen.NameDeclaration
		transportLayout      *goacodegen.GoTypePlan
		decode, encode       *goacodegen.TransformPlan
	)
	if shapes.usesTransport {
		if err := p.declareLocalTypes(p.transport, p.transportTypes, p.transportTypeUses, shapes.transportTypes); err != nil {
			return err
		}
		transportType := &goaexpr.UserTypeExpr{
			AttributeExpr: shapes.transport,
			TypeName:      preferred + "Transport",
		}
		transportAttribute = &goaexpr.AttributeExpr{Type: transportType}
		transportTypeDeclaration, declareErr := p.transport.DeclareGeneratedType(
			transportType.TypeName,
			specNameOrder{packagePath: p.transport.ImportPath(), key: key + ":transport"},
		)
		if declareErr != nil {
			return declareErr
		}
		if err := p.transport.BindGeneratedType(transportType, transportTypeDeclaration); err != nil {
			return err
		}
		transportDeclaration = transportTypeDeclaration.Declaration()
		p.transportTypeUses[transportType] = transportDeclaration
		if err := declareAttributeUnions(p.transport, p.transportFixed, p.transportUnionErrors, shapes.transport); err != nil {
			return err
		}
		transportLayout, err = p.planDeclaredTypeLayout(
			transportAttribute,
			p.transport,
			goacodegen.GoLayoutPolicy{
				Pointer:             true,
				UnionPointer:        true,
				ArrayElementPointer: true,
				SumType:             true,
			},
		)
		if err != nil {
			return err
		}
		decode, err = p.declareTransform(key+":decode", transportAttribute, publicAttribute, "decode")
		if err != nil {
			return err
		}
		encode, err = p.declareTransform(key+":encode", publicAttribute, transportAttribute, "encode")
		if err != nil {
			return err
		}
	}
	names, err := p.declareTypeNames(
		key,
		preferred,
		owner.Kind == contractTypeOwnerCompletion,
		publicDeclaration,
		transportDeclaration,
	)
	if err != nil {
		return err
	}
	jsonValidator, err := p.declareJSONValidator(key, preferred, cloneModelSchemaAttribute(shapes.transport), owner, usage)
	if err != nil {
		return err
	}
	p.types[identity] = &plannedSpecType{
		publicDeclaration:    publicDeclaration,
		transportDeclaration: transportDeclaration,
		publicLayout:         publicLayout,
		transportLayout:      transportLayout,
		publicShape:          public,
		transportShape:       shapes.transport,
		publicTypes:          publicTypes,
		transportTypes:       shapes.transportTypes,
		public:               publicAttribute,
		transport:            transportAttribute,
		decode:               decode,
		encode:               encode,
		exportedCodec:        names.exportedCodec,
		genericCodec:         names.genericCodec,
		marshal:              names.marshal,
		unmarshal:            names.unmarshal,
		transportValidator:   names.transportValidator,
		fieldDescriptions:    names.fieldDescriptions,
		fieldJSONTypes:       names.fieldJSONTypes,
		enrichValidation:     names.enrichValidation,
		invalidFieldType:     names.invalidFieldType,
		jsonValidator:        jsonValidator,
	}
	if shapes.usesTransport {
		p.setTransformLayouts(decode, transportLayout, publicLayout)
		p.setTransformLayouts(encode, publicLayout, transportLayout)
	}
	return nil
}

// declareTypeImports records the packages written by the generated type,
// codec, union, and validation files before Goa chooses their final names.
func (p *toolSpecsPackagePlan) declareTypeImports(owner *contractTypeOwner, attribute *goaexpr.AttributeExpr, usage typeUsage) error {
	shapes := localizedSpecShapes(owner, attribute, usage)
	for _, localized := range shapes.publicTypes {
		if err := p.fileImports.publicTypes.AddTypeExpressions(localized.generated.AttributeExpr); err != nil {
			return err
		}
	}
	if err := p.fileImports.publicTypes.AddTypeExpressions(shapes.public); err != nil {
		return err
	}
	if err := p.fileImports.publicCodecs.AddRecursiveTypeReferences(shapes.public, shapes.transport); err != nil {
		return err
	}
	if err := p.fileImports.publicUnions.AddTypeExpressions(shapes.public); err != nil {
		return err
	}
	if !shapes.usesTransport {
		return nil
	}
	for _, localized := range shapes.transportTypes {
		if err := p.fileImports.transportTypes.AddTypeExpressions(localized.generated.AttributeExpr); err != nil {
			return err
		}
	}
	if err := p.fileImports.transportTypes.AddTypeExpressions(shapes.transport); err != nil {
		return err
	}
	return p.fileImports.transportUnions.AddTypeExpressions(shapes.transport)
}

// localizedSpecShapes copies one design type into the two package-specific
// forms emitted by Goa-AI. Import planning and declaration use this function,
// so a nested type cannot appear only after package names are fixed.
func localizedSpecShapes(owner *contractTypeOwner, attribute *goaexpr.AttributeExpr, usage typeUsage) localizedSpecTypeShapes {
	shape := attribute
	if userType, ok := attribute.Type.(goaexpr.UserType); ok && userType != goaexpr.Empty {
		shape = userType.Attribute()
	}
	public, publicTypes := localizeNestedTypes(shape, false, nil)
	transportSource := public
	if usage == usagePayload && len(owner.ModelHiddenPayloadFields) > 0 {
		transportSource = modelTransportShape(public, owner.ModelHiddenPayloadFields)
	}
	transport := cloneWithModelJSONTags(transportSource)
	publicSources := make(map[goaexpr.UserType]goaexpr.UserType, len(publicTypes))
	for _, localized := range publicTypes {
		publicSources[localized.generated] = localized.source
	}
	transport, transportTypes := localizeNestedTypes(transport, true, publicSources)
	return localizedSpecTypeShapes{
		public:         public,
		publicTypes:    publicTypes,
		transport:      transport,
		transportTypes: transportTypes,
		usesTransport:  owner.Kind == contractTypeOwnerTool || goaexpr.IsObject(public.Type) || goaexpr.IsUnion(public.Type),
	}
}

// planDeclaredTypeLayout asks Goa how a generated named type is referenced.
// Later generator steps use the saved answer instead of inspecting rendered Go
// source for pointer characters.
func (p *toolSpecsPackagePlan) planDeclaredTypeLayout(
	attribute *goaexpr.AttributeExpr,
	pkg *goacodegen.GeneratedPackage,
	policy goacodegen.GoLayoutPolicy,
) (*goacodegen.GoTypePlan, error) {
	return goacodegen.PlanGoType(attribute, goacodegen.GoTypePlanOptions{
		Owner:            pkg.ImportPath(),
		Policy:           policy,
		RetainNamedValue: true,
		Bind: func(request goacodegen.GoTypeBindingRequest) (goacodegen.GoTypeBinding, error) {
			owner := request.InheritedOwner
			if location := goacodegen.UserTypeLocation(request.Attribute.Type); location != nil {
				owner = path.Join(p.genpkg, location.RelImportPath)
			}
			generated := p.generation.Package(owner)
			switch request.Kind {
			case goacodegen.GoNamed:
				declaration, err := generated.Type(request.Attribute.Type.(goaexpr.UserType))
				if err != nil {
					return goacodegen.GoTypeBinding{}, err
				}
				return goacodegen.GoTypeBinding{Owner: owner, Type: declaration}, nil
			case goacodegen.GoUnion:
				declaration, err := generated.Union(request.Attribute)
				if err != nil {
					return goacodegen.GoTypeBinding{}, err
				}
				return goacodegen.GoTypeBinding{Owner: owner, Union: declaration}, nil
			case goacodegen.GoPrimitive,
				goacodegen.GoArray,
				goacodegen.GoMap,
				goacodegen.GoStruct,
				goacodegen.GoEmpty,
				goacodegen.GoServiceError:
				return goacodegen.GoTypeBinding{}, fmt.Errorf("generated type layout requested unsupported %s", request.Kind)
			}
			panic(fmt.Sprintf("unknown generated type layout kind %d", request.Kind))
		},
	})
}

// declareTypeNames records the variables and functions written with one type.
func (p *toolSpecsPackagePlan) declareTypeNames(key, preferred string, completion bool, public, transport *goacodegen.NameDeclaration) (*plannedSpecType, error) {
	names := &plannedSpecType{}
	declarePublic := func(prefix, suffix, role string) (*goacodegen.NameDeclaration, error) {
		return p.public.DeclareDependentName(
			goacodegen.NameFunction,
			public,
			prefix,
			suffix,
			specNameOrder{packagePath: p.public.ImportPath(), key: key + ":" + role},
		)
	}
	declarePrivate := func(kind goacodegen.PackageNameKind, name, role string) (*goacodegen.NameDeclaration, error) {
		declaration := goacodegen.NewPreferredName(
			kind,
			name,
			goacodegen.UnexportedName,
			specNameOrder{packagePath: p.public.ImportPath(), key: key + ":" + role},
		)
		if err := p.public.DeclareName(declaration); err != nil {
			return nil, err
		}
		return declaration, nil
	}
	var err error
	codecPrefix := ""
	if completion {
		codecPrefix = "new"
	}
	names.exportedCodec, err = declarePublic(codecPrefix, "Codec", "codec")
	if err != nil {
		return nil, err
	}
	names.genericCodec, err = declarePrivate(goacodegen.NameVariable, lowerCamel(preferred)+"Codec", "generic-codec")
	if err != nil {
		return nil, err
	}
	marshalPrefix := "Marshal"
	unmarshalPrefix := "Unmarshal"
	if completion {
		marshalPrefix = "marshal"
		unmarshalPrefix = "unmarshal"
	}
	names.marshal, err = declarePublic(marshalPrefix, "", "marshal")
	if err != nil {
		return nil, err
	}
	names.unmarshal, err = declarePublic(unmarshalPrefix, "", "unmarshal")
	if err != nil {
		return nil, err
	}
	names.fieldDescriptions, err = declarePrivate(
		goacodegen.NameVariable,
		lowerCamel(preferred)+"FieldDescs",
		"field-descriptions",
	)
	if err != nil {
		return nil, err
	}
	names.fieldJSONTypes, err = declarePrivate(
		goacodegen.NameVariable,
		lowerCamel(preferred)+"FieldJSONTypes",
		"field-json-types",
	)
	if err != nil {
		return nil, err
	}
	names.enrichValidation, err = declarePublic("enrich", "ValidationError", "enrich-validation")
	if err != nil {
		return nil, err
	}
	names.invalidFieldType, err = declarePublic("invalid", "FieldTypeError", "invalid-field-type")
	if err != nil {
		return nil, err
	}
	if transport != nil {
		names.transportValidator, err = p.transport.DeclareDependentName(
			goacodegen.NameFunction,
			transport,
			"Validate",
			"",
			specNameOrder{packagePath: p.transport.ImportPath(), key: key + ":validate"},
		)
		if err != nil {
			return nil, err
		}
	}
	return names, nil
}

// declareLocalTypes records the nested types that one output package writes.
func (p *toolSpecsPackagePlan) declareLocalTypes(pkg *goacodegen.GeneratedPackage, declared map[goaexpr.UserType]*goacodegen.TypeDeclaration, uses map[goaexpr.UserType]*goacodegen.NameDeclaration, types []*localizedType) error {
	for _, localized := range types {
		if declaration := declared[localized.source]; declaration != nil {
			if err := pkg.BindGeneratedType(localized.generated, declaration); err != nil {
				return err
			}
			localized.declaration = declaration
			uses[localized.generated] = declaration.Declaration()
			continue
		}
		declaration, err := pkg.DeclareGeneratedType(
			localized.generated.TypeName,
			newLocalizedTypeNameOrder(pkg.ImportPath(), localized.source, localizedTypeDeclarationName),
		)
		if err != nil {
			return err
		}
		if err := pkg.BindGeneratedType(localized.generated, declaration); err != nil {
			return err
		}
		declared[localized.source] = declaration
		localized.declaration = declaration
		uses[localized.generated] = declaration.Declaration()
		if pkg == p.transport {
			validator, err := pkg.DeclareDependentName(
				goacodegen.NameFunction,
				declaration.Declaration(),
				"Validate",
				"",
				newLocalizedTypeNameOrder(pkg.ImportPath(), localized.source, localizedTypeValidatorName),
			)
			if err != nil {
				return err
			}
			p.transportValidators[localized.source] = validator
		}
	}
	return nil
}

// declareTransform records the helper functions needed to copy one generated
// type into another and saves each chosen name with its function.
func (p *toolSpecsPackagePlan) declareTransform(key string, source, target *goaexpr.AttributeExpr, prefix string) (*goacodegen.TransformPlan, error) {
	planned, err := p.recordTransform(key, source, target, prefix)
	if err != nil {
		return nil, err
	}
	return planned.plan, nil
}

// declareAdapterTransform records one conversion written to transforms.go.
func (p *toolSpecsPackagePlan) declareAdapterTransform(key string, source, target *goaexpr.AttributeExpr) (*goacodegen.TransformPlan, error) {
	planned, err := p.recordTransform(key, source, target, "")
	if err != nil {
		return nil, err
	}
	p.adapterTransformPlans = append(p.adapterTransformPlans, planned)
	return planned.plan, nil
}

// recordTransform saves one conversion for package-wide helper planning.
func (p *toolSpecsPackagePlan) recordTransform(key string, source, target *goaexpr.AttributeExpr, prefix string) (*plannedPackageTransform, error) {
	planned, err := goacodegen.NewTransformPlan(source, target, prefix, nil)
	if err != nil {
		return nil, err
	}
	transform := &plannedPackageTransform{
		key:    key,
		prefix: prefix,
		plan:   planned,
	}
	p.transformPlans = append(p.transformPlans, transform)
	return transform, nil
}

// declareAttributeUnions records every union reachable from attribute once.
func declareAttributeUnions(pkg *goacodegen.GeneratedPackage, fixed map[string]*goacodegen.NameDeclaration, helpers map[goacodegen.UnionDeclarationID]*goacodegen.NameDeclaration, attribute *goaexpr.AttributeExpr) error {
	seenTypes := make(map[goaexpr.UserType]struct{})
	seenUnions := make(map[goacodegen.UnionDeclarationID]struct{})
	var visit func(*goaexpr.AttributeExpr) error
	visit = func(current *goaexpr.AttributeExpr) error {
		if current == nil || current.Type == nil || current.Type == goaexpr.Empty {
			return nil
		}
		switch actual := current.Type.(type) {
		case goaexpr.UserType:
			if goacodegen.UserTypeLocation(actual) != nil {
				return nil
			}
			origin := actual.Origin()
			if _, ok := seenTypes[origin]; ok {
				return nil
			}
			seenTypes[origin] = struct{}{}
			return visit(actual.Attribute())
		case *goaexpr.Object:
			for _, field := range *actual {
				if err := visit(field.Attribute); err != nil {
					return err
				}
			}
		case *goaexpr.Array:
			return visit(actual.ElemType)
		case *goaexpr.Map:
			if err := visit(actual.KeyType); err != nil {
				return err
			}
			return visit(actual.ElemType)
		case *goaexpr.Union:
			if err := declareExactNames(pkg, fixed, map[goacodegen.PackageNameKind][]string{
				goacodegen.NameFunction: {"decodeUnionStrictJSON", "missingUnionValueError", "nullUnionValueError"},
			}); err != nil {
				return err
			}
			identity := goacodegen.NewUnionDeclarationID(current)
			if _, ok := seenUnions[identity]; ok {
				return nil
			}
			seenUnions[identity] = struct{}{}
			declaration, err := pkg.DeclareUnion(current)
			if err != nil {
				return err
			}
			if helpers[identity] == nil {
				helper, err := pkg.DeclareDependentName(
					goacodegen.NameFunction,
					declaration.Declaration(),
					"new",
					"DiscriminatorError",
					unionErrorNameOrder{packagePath: pkg.ImportPath(), unionName: actual.Name()},
				)
				if err != nil {
					return err
				}
				helpers[identity] = helper
			}
			for _, branch := range actual.Values {
				if err := visit(branch.Attribute); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(attribute)
}
