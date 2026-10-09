// Package mcp supplies strict JSON parsing to generated boundary decoders.
// Exact names, duplicate rejection and declared non-null values apply to OAuth
// and known Skill frontmatter fields; generated Goa code owns their constraints.
package mcp

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"net/http"

	goahttp "goa.design/goa/v3/http"
)

var generatedValueUnmarshalers = json.UnmarshalFromFunc[any](rejectDeclaredJSONNull)

// generatedJSONDecoder rejects duplicate names, invalid text and trailing
// data before generated validation. Unknown extension fields remain open and
// are skipped without changing the caller's separately retained raw content.
func generatedJSONDecoder(response *http.Response) goahttp.Decoder {
	return goahttp.EncodingFunc(func(value any) error {
		return json.UnmarshalRead(response.Body, value, json.WithUnmarshalers(generatedValueUnmarshalers))
	})
}

// rejectDeclaredJSONNull rejects null for a declared typed field, then lets
// the standard decoder handle other values. Unknown fields never enter this
// hook, so future extension fields can retain explicit null values.
func rejectDeclaredJSONNull(decoder *jsontext.Decoder, _ any) error {
	if decoder.PeekKind() == 'n' {
		return errors.New("declared JSON values cannot be null")
	}
	return errors.ErrUnsupported
}
