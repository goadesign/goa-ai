// This file implements the synthetic resource reader used by the protocol
// referee. The service owns which URI families exist; discovery templates do
// not reconstruct variables or grant access to a resource.
package assistantapi

import (
	"context"
	"strings"

	genassistant "example.com/assistant/gen/assistant"
	goa "goa.design/goa/v3/pkg"
)

// ReadResource returns synthetic contents for the exact supplied URI or reports
// an unknown resource. It never opens a file or fetches a client-supplied address.
func (s *assistantsrvc) ReadResource(_ context.Context, p *genassistant.ReadResourcePayload) (*genassistant.ReadResourceResult, error) {
	item := &genassistant.TemplateItem{}
	switch {
	case strings.HasPrefix(p.Address, "test://template/") && strings.HasSuffix(p.Address, "/data"), strings.HasPrefix(p.Address, "test://reserved/"):
		mimeType := "text/plain"
		item.Selected.SetText(&genassistant.TemplateText{
			URI: p.Address, MimeType: &mimeType, Text: "Data for requested URI: " + p.Address,
		})
	case strings.HasPrefix(p.Address, "test://binary/"):
		mimeType := "application/octet-stream"
		item.Selected.SetBlob(&genassistant.TemplateBlob{
			URI: p.Address, MimeType: &mimeType, Blob: []byte{1, 2},
		})
	default:
		return nil, goa.PermanentError("invalid_params", "resource does not exist: %s", p.Address)
	}
	return &genassistant.ReadResourceResult{Parts: []*genassistant.TemplateItem{item}}, nil
}
