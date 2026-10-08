// Package codegen binds authored content metadata to the shared JSON codecs.
// Resource readers, prompts and tools retain their service types and selected
// result views; only the open MCP metadata field receives encoded JSON.
package codegen

import (
	"fmt"

	jsoncodec "goa.design/goa-ai/codegen/internal/codec"
	"goa.design/goa/v3/codegen"
	goaservice "goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/expr"
)

type (
	// contentMetadataData records the final field names and encoder for one
	// authored object. Content branches and catalog entries share these facts.
	contentMetadataData struct {
		// Field names the object in the service or selected result view.
		Field string
		// Encode calls the shared private codec for the authored type.
		Encode string
		// Optional omits absent objects instead of encoding a JSON null value.
		Optional bool
		// TargetField holds the encoded metadata in the protocol record.
		TargetField string
	}
)

// bindContentMetadataCodecs supplies the owning service or view type writer to
// each planned metadata encoder before its private codec package is rendered.
func bindContentMetadataCodecs(services *goaservice.ServicesData, planned *plannedMCPService) error {
	data := planned.adapterData
	bound := make(map[*jsoncodec.Value]struct{})

	if reader := data.ResourceReader; reader != nil {
		if err := bindOwnedContentMetadata(services, planned, reader.method, reader.conversion, bound); err != nil {
			return err
		}
	}
	for _, prompt := range data.MethodPrompts {
		if err := bindOwnedContentMetadata(services, planned, prompt.prompt.Method, prompt.conversion, bound); err != nil {
			return err
		}
	}
	for _, tool := range data.Tools {
		if tool.Content == nil {
			continue
		}
		owner := tool.Content.tool.Method
		if tool.Task != nil {
			owner = tool.Task.binding.Read
		}
		for _, selected := range tool.Content.Cases {
			if selected.conversion == nil {
				continue
			}
			if err := bindOwnedContentMetadata(services, planned, owner, selected.conversion, bound); err != nil {
				return err
			}
		}
	}
	for _, catalog := range []*catalogAdapter{data.ResourceCatalog, data.ResourceTemplateCatalog} {
		if catalog == nil || catalog.metaCodec == nil {
			continue
		}
		writer := services.ServiceAttributor(planned.prepared.userService.Name, data.CodecImportPath)
		if _, viewed := catalog.method.Result.Type.(*expr.ResultTypeExpr); viewed {
			writer = services.ViewAttributor(planned.prepared.userService.Name, data.CodecImportPath)
		}
		if err := catalog.metaCodec.BindService(writer); err != nil {
			return err
		}
	}
	return nil
}

// bindContentMetadata follows declared content branches and embedded resources.
// Shared converters bind an encoder once even when several prompts use it.
func bindContentMetadata(conversion *contentConversion, writer codegen.Attributor, bound map[*jsoncodec.Value]struct{}) error {
	for _, branch := range conversion.branches {
		if branch.metaCodec != nil {
			if _, exists := bound[branch.metaCodec]; !exists {
				if err := branch.metaCodec.BindService(writer); err != nil {
					return err
				}
				bound[branch.metaCodec] = struct{}{}
			}
		}
		if branch.nested != nil {
			if err := bindContentMetadata(branch.nested, writer, bound); err != nil {
				return err
			}
		}
	}
	return nil
}

// planContentMetadata uses the saved service field layout to plan one JSON
// encoder. Both content branches and catalog entries retain authored names.
func planContentMetadata(codecs *jsoncodec.Plan, attribute *expr.AttributeExpr, layout *codegen.GoTypePlan, name string) (*jsoncodec.Value, *codegen.GoTypePlan, error) {
	matches := layout.PlansForOccurrence(attribute)
	if len(matches) != 1 {
		return nil, nil, fmt.Errorf("content metadata has %d planned occurrences", len(matches))
	}
	value, err := codecs.Add(name, name, attribute, matches[0], jsoncodec.EncodeOnly)
	if err != nil {
		return nil, nil, err
	}
	return value, matches[0], nil
}

// planCatalogMetadata records the resource catalog encoders after the common
// private codec package exists. Ordinary raw metadata needs no extra encoder.
func planCatalogMetadata(codecs *jsoncodec.Plan, data *AdapterData) error {
	for _, catalog := range []*catalogAdapter{data.ResourceCatalog, data.ResourceTemplateCatalog} {
		if catalog == nil || catalog.metaAttribute == nil {
			continue
		}
		var err error
		catalog.metaCodec, catalog.metaLayout, err = planContentMetadata(codecs, catalog.metaAttribute, catalog.Endpoint.resultLayout, catalog.collection+"Metadata")
		if err != nil {
			return err
		}
	}
	return nil
}

// bindOwnedContentMetadata chooses the original service or selected view that
// owns the supplied content, then binds its encoders before rendering codecs.
func bindOwnedContentMetadata(services *goaservice.ServicesData, planned *plannedMCPService, method *expr.MethodExpr, conversion *contentConversion, bound map[*jsoncodec.Value]struct{}) error {
	writer := services.ServiceAttributor(planned.prepared.userService.Name, planned.adapterData.CodecImportPath)
	if _, viewed := method.Result.Type.(*expr.ResultTypeExpr); viewed {
		writer = services.ViewAttributor(planned.prepared.userService.Name, planned.adapterData.CodecImportPath)
	}
	return bindContentMetadata(conversion, writer, bound)
}
