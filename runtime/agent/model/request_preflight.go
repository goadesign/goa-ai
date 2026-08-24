// Package model bounds model requests before the framework copies any caller-
// supplied bytes. The validated client uses one request-wide byte and value
// budget across messages, media, tool contracts, and structured output.
package model

import (
	"fmt"
	"reflect"
)

// preflightRequest checks every mutable request field before cloneRequest
// allocates its framework-owned copy.
func preflightRequest(request *Request) error {
	if request == nil {
		return fmt.Errorf("model request is required")
	}
	walk := &dynamicValueWalk{}
	if err := walk.visit(); err != nil {
		return err
	}
	if err := chargeString(walk, request.Model); err != nil {
		return fmt.Errorf("model request model: %w", err)
	}
	if err := chargeString(walk, string(request.ModelClass)); err != nil {
		return fmt.Errorf("model request class: %w", err)
	}
	if err := walk.checkChildren(len(request.PromptRefs)); err != nil {
		return fmt.Errorf("model request prompt references: %w", err)
	}
	for index, ref := range request.PromptRefs {
		if err := walk.visit(); err != nil {
			return fmt.Errorf("model request prompt reference %d: %w", index, err)
		}
		if err := chargeString(walk, string(ref.ID)); err != nil {
			return fmt.Errorf("model request prompt reference %d ID: %w", index, err)
		}
		if err := chargeString(walk, ref.Version); err != nil {
			return fmt.Errorf("model request prompt reference %d version: %w", index, err)
		}
	}
	if err := walk.checkChildren(len(request.Messages)); err != nil {
		return fmt.Errorf("model request messages: %w", err)
	}
	for index, message := range request.Messages {
		if message == nil {
			return fmt.Errorf("model request message %d is nil", index)
		}
		if err := preflightRequestMessage(message, walk); err != nil {
			return fmt.Errorf("model request message %d: %w", index, err)
		}
	}
	if err := preflightRequestTools(request.Tools, walk); err != nil {
		return err
	}
	if request.ToolChoice != nil {
		if err := walk.visit(); err != nil {
			return fmt.Errorf("model request tool choice: %w", err)
		}
		if err := chargeString(walk, string(request.ToolChoice.Mode)); err != nil {
			return fmt.Errorf("model request tool choice mode: %w", err)
		}
		if err := chargeString(walk, request.ToolChoice.Name); err != nil {
			return fmt.Errorf("model request tool choice name: %w", err)
		}
	}
	if request.Thinking != nil {
		if err := walk.visit(); err != nil {
			return fmt.Errorf("model request thinking options: %w", err)
		}
	}
	if request.StructuredOutput != nil {
		if err := preflightStructuredOutput(request.StructuredOutput, walk); err != nil {
			return err
		}
	}
	if request.Cache != nil {
		if err := walk.visit(); err != nil {
			return fmt.Errorf("model request cache options: %w", err)
		}
	}
	return nil
}

// preflightRequestTools bounds tool names, descriptions, schemas, and examples
// before cloneRequest allocates the tool slice or copies raw JSON.
func preflightRequestTools(definitions []*ToolDefinition, walk *dynamicValueWalk) error {
	if err := walk.checkChildren(len(definitions)); err != nil {
		return fmt.Errorf("model request tools: %w", err)
	}
	for index, definition := range definitions {
		if definition == nil {
			return fmt.Errorf("model request tool %d is nil", index)
		}
		if err := walk.visit(); err != nil {
			return fmt.Errorf("model request tool %d: %w", index, err)
		}
		if err := chargeString(walk, definition.Name); err != nil {
			return fmt.Errorf("model request tool %d name: %w", index, err)
		}
		if err := chargeString(walk, definition.Description); err != nil {
			return fmt.Errorf("model request tool %d description: %w", index, err)
		}
		for _, field := range []struct {
			label  string
			value  []byte
			schema bool
		}{
			{label: "schema", value: definition.Input.jsonSchema, schema: true},
			{
				label:  "schema without root example",
				value:  definition.Input.schemaWithoutRootExample,
				schema: true,
			},
			{label: "example", value: definition.Input.exampleJSON},
		} {
			if field.schema && len(field.value) > maxToolSchemaBytes {
				return fmt.Errorf(
					"model request tool %d %s uses %d bytes, maximum is %d",
					index,
					field.label,
					len(field.value),
					maxToolSchemaBytes,
				)
			}
			if err := chargeJSON(walk, field.value); err != nil {
				return fmt.Errorf("model request tool %d %s: %w", index, field.label, err)
			}
		}
	}
	return nil
}

// preflightStructuredOutput bounds one provider-enforced completion contract.
func preflightStructuredOutput(output *StructuredOutput, walk *dynamicValueWalk) error {
	if err := walk.visit(); err != nil {
		return fmt.Errorf("model request structured output: %w", err)
	}
	for _, field := range []struct {
		label string
		value []byte
	}{
		{label: "schema", value: output.Schema},
		{label: "schema without root example", value: output.SchemaWithoutRootExample},
		{label: "example", value: output.ExampleJSON},
	} {
		if err := chargeJSON(walk, field.value); err != nil {
			return fmt.Errorf("model request structured output %s: %w", field.label, err)
		}
	}
	if err := chargeString(walk, output.Name); err != nil {
		return fmt.Errorf("model request structured output name: %w", err)
	}
	if err := chargeString(walk, output.Description); err != nil {
		return fmt.Errorf("model request structured output description: %w", err)
	}
	return nil
}

// preflightRequestMessage bounds one transcript message and every nested part.
func preflightRequestMessage(message *Message, walk *dynamicValueWalk) error {
	if err := walk.visit(); err != nil {
		return err
	}
	if err := chargeString(walk, string(message.Role)); err != nil {
		return fmt.Errorf("role: %w", err)
	}
	if err := walk.checkChildren(len(message.Parts)); err != nil {
		return fmt.Errorf("parts: %w", err)
	}
	for index, part := range message.Parts {
		if err := preflightRequestPart(part, walk); err != nil {
			return fmt.Errorf("part %d: %w", index, err)
		}
	}
	if message.Meta != nil {
		if err := preflightDynamicValueAt(
			reflect.ValueOf(message.Meta),
			0,
			walk,
			dynamicCloneCanonical,
		); err != nil {
			return fmt.Errorf("metadata: %w", err)
		}
	}
	return nil
}

// preflightRequestPart bounds one canonical transcript part without copying it.
func preflightRequestPart(part Part, walk *dynamicValueWalk) error {
	if err := walk.visit(); err != nil {
		return err
	}
	switch actual := part.(type) {
	case TextPart:
		return chargeString(walk, actual.Text)
	case ImagePart:
		if err := chargeString(walk, string(actual.Format)); err != nil {
			return err
		}
		return walk.addBytes(len(actual.Bytes))
	case DocumentPart:
		return preflightDocumentPart(actual, walk)
	case CitationsPart:
		return preflightCitationsPart(actual, walk)
	case ThinkingPart:
		if err := chargeString(walk, actual.Text); err != nil {
			return err
		}
		if err := chargeString(walk, actual.Signature); err != nil {
			return err
		}
		return walk.addBytes(len(actual.Redacted))
	case ToolUsePart:
		if err := chargeString(walk, actual.ID); err != nil {
			return err
		}
		if err := chargeString(walk, actual.Name); err != nil {
			return err
		}
		if err := chargeJSON(walk, actual.Input); err != nil {
			return err
		}
		return chargeString(walk, actual.ThoughtSignature)
	case ToolResultPart:
		if err := chargeString(walk, actual.ToolUseID); err != nil {
			return err
		}
		return preflightDynamicValueAt(
			reflect.ValueOf(actual.Content),
			0,
			walk,
			dynamicCloneCanonical,
		)
	case CacheCheckpointPart:
		return nil
	case nil:
		return fmt.Errorf("part is nil")
	default:
		return fmt.Errorf("unsupported message part type %T", part)
	}
}

// preflightDocumentPart bounds an uploaded, inline, chunked, or remote document.
func preflightDocumentPart(part DocumentPart, walk *dynamicValueWalk) error {
	for _, field := range []struct {
		label string
		value string
	}{
		{label: "name", value: part.Name},
		{label: "format", value: string(part.Format)},
		{label: "text", value: part.Text},
		{label: "URI", value: part.URI},
		{label: "context", value: part.Context},
	} {
		if err := chargeString(walk, field.value); err != nil {
			return fmt.Errorf("%s: %w", field.label, err)
		}
	}
	if err := walk.addBytes(len(part.Bytes)); err != nil {
		return err
	}
	if err := walk.checkChildren(len(part.Chunks)); err != nil {
		return err
	}
	for _, chunk := range part.Chunks {
		if err := walk.visit(); err != nil {
			return err
		}
		if err := chargeString(walk, chunk); err != nil {
			return err
		}
	}
	return nil
}

// preflightCitationsPart bounds generated text and all cited source strings.
func preflightCitationsPart(part CitationsPart, walk *dynamicValueWalk) error {
	if err := chargeString(walk, part.Text); err != nil {
		return err
	}
	if err := walk.checkChildren(len(part.Citations)); err != nil {
		return err
	}
	for _, citation := range part.Citations {
		if err := walk.visit(); err != nil {
			return err
		}
		if err := chargeString(walk, citation.Title); err != nil {
			return err
		}
		if err := chargeString(walk, citation.Source); err != nil {
			return err
		}
		if err := walk.checkChildren(len(citation.SourceContent)); err != nil {
			return err
		}
		for _, content := range citation.SourceContent {
			if err := walk.visit(); err != nil {
				return err
			}
			if err := chargeString(walk, content); err != nil {
				return err
			}
		}
		for _, present := range []bool{
			citation.Location.DocumentChar != nil,
			citation.Location.DocumentChunk != nil,
			citation.Location.DocumentPage != nil,
		} {
			if present {
				if err := walk.visit(); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
