// Package mcp supplies OAuth form encoding to native generated grant clients.
// Goa owns each request body's fields and types; these encoders implement the
// external form representation without JSON DTOs or runtime field reflection.
package mcp

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	gentokenclient "goa.design/goa-ai/internal/mcpauth/gen/http/access_tokens/client"
	goahttp "goa.design/goa/v3/http"
)

// codeFormEncoder writes the generated authorization-code body, keeping the
// verifier in the token request body and retaining the original redirect.
func codeFormEncoder(request *http.Request) goahttp.Encoder {
	return goahttp.EncodingFunc(func(value any) error {
		bodyPointer, ok := value.(**gentokenclient.CodeRequestBody)
		if !ok {
			return errors.New("mcp: authorization-code form requires its generated request body")
		}
		body := *bodyPointer
		writeGrantForm(request, url.Values{
			"grant_type": {"authorization_code"}, "client_id": {body.ClientID},
			"code": {body.Code}, "code_verifier": {body.CodeVerifier},
			"redirect_uri": {body.RedirectURI}, "resource": {body.Resource},
		})
		return nil
	})
}

// refreshFormEncoder writes the generated refresh body without putting a refresh
// credential in the URL, an MCP body, or the resource's Authorization header.
func refreshFormEncoder(request *http.Request) goahttp.Encoder {
	return goahttp.EncodingFunc(func(value any) error {
		bodyPointer, ok := value.(**gentokenclient.RefreshRequestBody)
		if !ok {
			return errors.New("mcp: refresh form requires its generated request body")
		}
		body := *bodyPointer
		writeGrantForm(request, url.Values{
			"grant_type": {"refresh_token"}, "client_id": {body.ClientID},
			"refresh_token": {body.RefreshToken}, "resource": {body.Resource},
		})
		return nil
	})
}

// writeGrantForm supplies the common form body and headers after a grant-specific
// encoder has selected its exact generated fields. No encoder can add URL fields.
func writeGrantForm(request *http.Request, form url.Values) {
	encoded := form.Encode()
	request.Body = io.NopCloser(strings.NewReader(encoded))
	request.ContentLength = int64(len(encoded))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
}
