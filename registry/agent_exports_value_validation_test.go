// These tests compare declaration field checks with JSON encoding so registration
// can reject invalid input without creating a JSON document it never saves.
package registry

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	goa "goa.design/goa/v3/pkg"
)

func TestAgentExportsValueValidationParity(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		change func(*genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration
		valid  bool
	}{
		{"complete", nil, true},
		{"nil declaration", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration { return nil }, false},
		{"missing exports", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports = nil
			return p
		}, false},
		{"empty exports", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports = []*genregistry.ToolSchema{}
			return p
		}, false},
		{"nil tool", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0] = nil
			return p
		}, false},
		{"empty scope", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Scope = ""
			return p
		}, false},
		{"empty resource", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.ResourceName = ""
			return p
		}, false},
		{"empty route", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Name = ""
			return p
		}, false},
		{"invalid version", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			version := genregistry.SemVer("bad")
			p.Version = &version
			return p
		}, false},
		{"invalid UTF8 string", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Scope = string([]byte{0xff})
			return p
		}, false},
		{"invalid UTF8 map key", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].ConsumerContract.Meta = map[string][]string{string([]byte{0xff}): {"value"}}
			return p
		}, false},
		{"missing schema", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].PayloadSchema = nil
			return p
		}, false},
		{"empty schema", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].PayloadSchema = []byte{}
			return p
		}, false},
		{"missing search", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].ConsumerContract.Search = nil
			return p
		}, false},
		{"missing metadata", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].ConsumerContract.Payload = nil
			return p
		}, false},
		{"empty title", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].ConsumerContract.Title = ""
			return p
		}, false},
		{"negative search length", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].ConsumerContract.Search.Length = -1
			return p
		}, false},
		{"wide negative search length", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].ConsumerContract.Search.Length = math.MinInt + 1
			return p
		}, false},
		{"wide negative term count", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].ConsumerContract.Search.Terms["installation"] = math.MinInt + 1
			return p
		}, false},
		{"empty optional metadata", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Tags = []string{}
			p.Exports[0].ConsumerContract.Meta = map[string][]string{}
			return p
		}, true},
		{"schema only", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].ConsumerContract = nil
			return p
		}, true},
		{"paging", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports = testAgentPagingToolset().Tools
			return p
		}, true},
		{"escaped Unicode", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			description := "A <record> & \"quoted\" value\n雪"
			p.Description = &description
			p.Exports[0].ConsumerContract.Title = description
			return p
		}, true},
		{"nested tagged union", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].ConsumerContract.Payload.Fields = taggedValueValidationFields()
			return p
		}, true},
		{"unset nested union", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].ConsumerContract.Payload.Fields = []*genregistry.ToolFieldMetadata{{Branches: []*genregistry.ToolUnionBranch{{}}}}
			return p
		}, false},
		{"nil selected branch", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].ConsumerContract.Payload.Fields = []*genregistry.ToolFieldMetadata{{Branches: []*genregistry.ToolUnionBranch{{Selection: genregistry.NewToolUnionSelectionTagged(nil)}}}}
			return p
		}, false},
		{"empty tagged branch", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].ConsumerContract.Payload.Fields = []*genregistry.ToolFieldMetadata{{Branches: []*genregistry.ToolUnionBranch{{Selection: genregistry.NewToolUnionSelectionTagged(&genregistry.ToolTaggedUnionBranch{})}}}}
			return p
		}, false},
		{"unset field segment", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].ConsumerContract.Payload.Fields = []*genregistry.ToolFieldMetadata{{Path: []*genregistry.ToolFieldPathSegment{{}}}}
			return p
		}, false},
		{"untagged zero index", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].ConsumerContract.Payload.Fields = untaggedValueValidationFields(0)
			return p
		}, true},
		{"untagged negative index", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].ConsumerContract.Payload.Fields = untaggedValueValidationFields(-1)
			return p
		}, false},
		{"untagged wide negative index", func(p *genregistry.AgentExportsDeclaration) *genregistry.AgentExportsDeclaration {
			p.Exports[0].ConsumerContract.Payload.Fields = untaggedValueValidationFields(math.MinInt + 1)
			return p
		}, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			build := func() *genregistry.AgentExportsDeclaration {
				toolset := testAgentToolset("v1")
				p := &genregistry.AgentExportsDeclaration{Scope: "owner", ResourceName: "native", Name: toolset.Name, Exports: toolset.Tools}
				if fixture.change != nil {
					p = fixture.change(p)
				}
				return p
			}
			p := build()
			original := build()
			_, encodedErr := genregistry.EncodeAgentExportsDeclaration(p)
			assert.Equal(t, original, p, "encoding must preserve the submitted value")
			validationErr := genregistry.ValidateAgentExportsDeclarationValue(p)
			assert.Equal(t, fixture.valid, encodedErr == nil, "encoding: %v", encodedErr)
			assert.Equal(t, fixture.valid, validationErr == nil, "validation: %v", validationErr)
			assert.Equal(t, original, p, "validation must preserve the submitted value")
			if !fixture.valid {
				service := &Service{}
				result, err := service.RegisterAgentExports(t.Context(), p)
				assert.Nil(t, result)
				var validationError *goa.ServiceError
				if assert.ErrorAs(t, err, &validationError) {
					assert.Equal(t, genregistry.MakeValidationError(encodedErr).Name, validationError.Name)
				}
			}
		})
	}
}

// taggedValueValidationFields supplies a discriminator path and selected value;
// callers receive a complete nested branch with an allowed empty field name.
func taggedValueValidationFields() []*genregistry.ToolFieldMetadata {
	return []*genregistry.ToolFieldMetadata{{Branches: []*genregistry.ToolUnionBranch{{
		Selection: genregistry.NewToolUnionSelectionTagged(&genregistry.ToolTaggedUnionBranch{
			Discriminator: []*genregistry.ToolFieldPathSegment{{Segment: genregistry.NewToolFieldSegmentField("")}},
			Value:         "selected",
		}),
	}}}}
}

// untaggedValueValidationFields puts the supplied index in a selected object
// branch so callers can check that its original integer is not negative.
func untaggedValueValidationFields(index int) []*genregistry.ToolFieldMetadata {
	return []*genregistry.ToolFieldMetadata{{Branches: []*genregistry.ToolUnionBranch{{
		Selection: genregistry.NewToolUnionSelectionUntagged(&genregistry.ToolUntaggedUnionBranch{JSONKind: "object", Index: index}),
	}}}}
}
