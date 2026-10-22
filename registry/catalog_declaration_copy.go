// Package registry copies prepared saved declarations without changing their
// generated service values. Each reader owns all pointers, maps, slices and raw
// schema bytes; integer widths and nil or empty collections remain unchanged.
package registry

import (
	"fmt"
	"maps"
	"slices"

	genregistry "goa.design/goa-ai/registry/gen/registry"
)

// copyToolsetDeclaration copies the already checked saved declaration. Keeping
// these generated service types avoids transport conversions changing its data.
func copyToolsetDeclaration(source *genregistry.Toolset) *genregistry.Toolset {
	result := *source
	result.Description = copyDeclarationString(source.Description)
	result.Version = copyDeclarationString(source.Version)
	result.Tags = slices.Clone(source.Tags)
	result.Tools = slices.Clone(source.Tools)
	for index, tool := range source.Tools {
		result.Tools[index] = copyToolDeclaration(tool)
	}
	return &result
}

// copyToolDeclaration separates schemas and consumer metadata from the retained
// declaration so a reader can change them without affecting another reader.
func copyToolDeclaration(source *genregistry.ToolSchema) *genregistry.ToolSchema {
	result := *source
	result.Description = copyDeclarationString(source.Description)
	result.Tags = slices.Clone(source.Tags)
	result.PayloadSchema = slices.Clone(source.PayloadSchema)
	result.ExecutionPayloadSchema = slices.Clone(source.ExecutionPayloadSchema)
	result.ResultSchema = slices.Clone(source.ResultSchema)
	result.SidecarSchema = slices.Clone(source.SidecarSchema)
	result.ConsumerContract = copyConsumerContract(source.ConsumerContract)
	return &result
}

// copyConsumerContract copies every optional execution and presentation value
// while retaining the exact generated scalar values and collection presence.
func copyConsumerContract(source *genregistry.ConsumerContract) *genregistry.ConsumerContract {
	if source == nil {
		return nil
	}
	result := *source
	result.Search = copyToolSearchDocument(source.Search)
	result.Payload = copyToolTypeMetadata(source.Payload)
	result.Result = copyToolTypeMetadata(source.Result)
	result.Meta = maps.Clone(source.Meta)
	for key, values := range source.Meta {
		result.Meta[key] = slices.Clone(values)
	}
	result.RequiredLabels = slices.Clone(source.RequiredLabels)
	if source.Bounds != nil {
		bounds := *source.Bounds
		if source.Bounds.Paging != nil {
			paging := *source.Bounds.Paging
			paging.ContinueTool = copyDeclarationString(paging.ContinueTool)
			paging.SourceTool = copyDeclarationString(paging.SourceTool)
			paging.RetainedFields = slices.Clone(paging.RetainedFields)
			bounds.Paging = &paging
		}
		result.Bounds = &bounds
	}
	if source.Confirmation != nil {
		confirmation := *source.Confirmation
		confirmation.Title = copyDeclarationString(confirmation.Title)
		result.Confirmation = &confirmation
	}
	result.ServerData = slices.Clone(source.ServerData)
	for index, data := range source.ServerData {
		if data != nil {
			owned := *data
			owned.Description = copyDeclarationString(data.Description)
			owned.Schema = slices.Clone(data.Schema)
			owned.Type = copyToolTypeMetadata(data.Type)
			result.ServerData[index] = &owned
		}
	}
	result.ResultReminder = copyDeclarationString(source.ResultReminder)
	if source.Agent != nil {
		agent := *source.Agent
		result.Agent = &agent
	}
	result.TextOnly = copyTextOnlyToolContract(source.TextOnly)
	return &result
}

// copyToolSearchDocument gives the reader its own word counts without converting
// the generated int values through a transport's narrower integer fields.
func copyToolSearchDocument(source *genregistry.ToolSearchDocument) *genregistry.ToolSearchDocument {
	if source == nil {
		return nil
	}
	result := *source
	result.Terms = maps.Clone(source.Terms)
	return &result
}

// copyTextOnlyToolContract copies the alternative instructions, schemas and
// metadata so changes to a reader's text-only view cannot affect preparation.
func copyTextOnlyToolContract(source *genregistry.TextOnlyToolContract) *genregistry.TextOnlyToolContract {
	if source == nil {
		return nil
	}
	result := *source
	result.Search = copyToolSearchDocument(source.Search)
	result.PayloadSchema = slices.Clone(source.PayloadSchema)
	result.Payload = copyToolTypeMetadata(source.Payload)
	result.ResultReminder = copyDeclarationString(source.ResultReminder)
	result.ExecutionSchema = slices.Clone(source.ExecutionSchema)
	return &result
}

// copyToolTypeMetadata copies examples, schemas and field descriptions. Optional
// nil entries in the generated field collection keep their saved positions.
func copyToolTypeMetadata(source *genregistry.ToolTypeMetadata) *genregistry.ToolTypeMetadata {
	if source == nil {
		return nil
	}
	result := *source
	result.Name = copyDeclarationString(source.Name)
	result.SchemaWithoutRootExample = slices.Clone(source.SchemaWithoutRootExample)
	result.ExampleJSON = slices.Clone(source.ExampleJSON)
	result.Fields = slices.Clone(source.Fields)
	for index, field := range source.Fields {
		if field != nil {
			owned := *field
			owned.Path = copyToolFieldPaths(field.Path)
			owned.JSONType = copyDeclarationString(field.JSONType)
			owned.Description = copyDeclarationString(field.Description)
			owned.DiscriminatorValues = slices.Clone(field.DiscriminatorValues)
			owned.Branches = slices.Clone(field.Branches)
			for branchIndex, branch := range field.Branches {
				if branch != nil {
					selection := copyToolUnionSelection(branch.Selection)
					owned.Branches[branchIndex] = &genregistry.ToolUnionBranch{Selection: selection}
				}
			}
			result.Fields[index] = &owned
		}
	}
	return &result
}

// copyToolFieldPaths keeps fixed property names and collection elements in their
// original order, with separate pointers for each returned path segment.
func copyToolFieldPaths(source []*genregistry.ToolFieldPathSegment) []*genregistry.ToolFieldPathSegment {
	result := slices.Clone(source)
	for index, segment := range source {
		if segment == nil {
			continue
		}
		var owned genregistry.ToolFieldSegment
		switch segment.Segment.Kind() {
		case genregistry.ToolFieldSegmentKindField:
			field, _ := segment.Segment.AsField()
			owned = genregistry.NewToolFieldSegmentField(field)
		case genregistry.ToolFieldSegmentKindElement:
			element, _ := segment.Segment.AsElement()
			copy := *element
			owned = genregistry.NewToolFieldSegmentElement(&copy)
		default:
			panic(fmt.Sprintf("invalid checked field segment %q", segment.Segment.Kind()))
		}
		result[index] = &genregistry.ToolFieldPathSegment{Segment: owned}
	}
	return result
}

// copyToolUnionSelection copies the selected generated branch and its paths.
// Saved JSON validation has already established one valid closed-union branch.
func copyToolUnionSelection(source genregistry.ToolUnionSelection) genregistry.ToolUnionSelection {
	switch source.Kind() {
	case genregistry.ToolUnionSelectionKindTagged:
		branch, _ := source.AsTagged()
		owned := *branch
		owned.Discriminator = copyToolFieldPaths(branch.Discriminator)
		return genregistry.NewToolUnionSelectionTagged(&owned)
	case genregistry.ToolUnionSelectionKindUntagged:
		branch, _ := source.AsUntagged()
		owned := *branch
		owned.Path = copyToolFieldPaths(branch.Path)
		return genregistry.NewToolUnionSelectionUntagged(&owned)
	default:
		panic(fmt.Sprintf("invalid checked union selection %q", source.Kind()))
	}
}

// copyDeclarationString preserves absent and explicit empty string values while
// returning a separate pointer, including for the generated semantic-version type.
func copyDeclarationString[T ~string](source *T) *T {
	if source == nil {
		return nil
	}
	return new(*source)
}
