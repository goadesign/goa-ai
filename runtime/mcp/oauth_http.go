// Package mcp adapts generated OAuth HTTP clients to discovered endpoint URLs
// and form requests. Goa owns field decoding and validation; these adapters own
// exact URL delivery, strict JSON names and credential-free transport errors.
package mcp

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	gentokenclient "goa.design/goa-ai/internal/mcpauth/gen/http/access_tokens/client"
	goahttp "goa.design/goa/v3/http"
)

type (
	// authorizationDoer sends generated requests to one exact discovered URL.
	// It rejects unexpected statuses before a generated decoder reads the body,
	// which can contain sensitive issuer diagnostics.
	authorizationDoer struct {
		client    *http.Client
		address   *url.URL
		operation string
	}
	// authorizationStatus retains only a status, never an issuer response body.
	authorizationStatus struct {
		code int
	}
)

var authorizationUnmarshalers = json.UnmarshalFromFunc[any](rejectAuthorizationNull)

// Do preserves escaped paths, raw queries and empty query markers when sending
// a generated request. Unsuccessful response bodies are closed without reading.
func (d *authorizationDoer) Do(request *http.Request) (response *http.Response, err error) {
	ctx, span := otel.Tracer("goa-ai/mcp").Start(request.Context(), "mcp.oauth.http.request", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	defer func() {
		if err != nil {
			if (d.operation == "resource_metadata" || d.operation == "issuer_metadata") && metadataMissing(err) {
				span.AddEvent("metadata location absent")
				return
			}
			safe := authorizationFailure(ctx, d.operation, err)
			span.RecordError(safe)
			span.SetStatus(codes.Error, safe.Error())
		}
	}()
	span.SetAttributes(attribute.String("oauth.operation", d.operation), attribute.String("http.request.method", request.Method))
	request = request.Clone(ctx)
	address := *d.address
	request.URL = &address
	request.Header.Set("Accept", "application/json")
	response, err = d.client.Do(request)
	if err != nil {
		return nil, err
	}
	span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
	if response.StatusCode != http.StatusOK {
		if err := response.Body.Close(); err != nil {
			return nil, errors.New("authorization response body could not be closed")
		}
		return nil, &authorizationStatus{code: response.StatusCode}
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return nil, errors.Join(errors.New("authorization endpoint did not return application/json"), response.Body.Close())
	}
	return response, nil
}

// Error returns the issuer's HTTP status without its response content.
func (e *authorizationStatus) Error() string {
	return fmt.Sprintf("authorization endpoint returned HTTP %d", e.code)
}

// authorizationDecoder applies exact JSON field names and rejects duplicate
// names, invalid UTF-8 and trailing data. Unknown OAuth extensions are ignored;
// the generated decoder then checks required fields and declared validations.
func authorizationDecoder(response *http.Response) goahttp.Decoder {
	return goahttp.EncodingFunc(func(value any) error {
		return json.UnmarshalRead(response.Body, value, json.WithUnmarshalers(authorizationUnmarshalers))
	})
}

// rejectAuthorizationNull rejects null for declared OAuth values, then lets
// the standard decoder handle every non-null value. Unknown extension fields
// are skipped by that decoder and never enter this type-specific hook.
func rejectAuthorizationNull(decoder *jsontext.Decoder, _ any) error {
	if decoder.PeekKind() == 'n' {
		return errors.New("declared authorization values cannot be null")
	}
	return errors.ErrUnsupported
}

// secretFormEncoder writes the generated token body using the OAuth endpoint's
// form contract. The secret is never copied to a URL or authorization header.
func secretFormEncoder(request *http.Request) goahttp.Encoder {
	return goahttp.EncodingFunc(func(value any) error {
		bodyPointer, ok := value.(**gentokenclient.SecretRequestBody)
		if !ok {
			return errors.New("mcp: client-secret form requires its generated request body")
		}
		body := *bodyPointer
		form := url.Values{
			"grant_type":    {"client_credentials"},
			"client_id":     {body.ClientID},
			"client_secret": {body.ClientSecret},
			"resource":      {body.Resource},
		}
		if body.Scope != nil {
			form.Set("scope", *body.Scope)
		}
		encoded := form.Encode()
		request.Body = io.NopCloser(strings.NewReader(encoded))
		request.ContentLength = int64(len(encoded))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return nil
	})
}

// metadataMissing permits only an absent endpoint to advance to the next
// specified discovery address. Invalid metadata and other HTTP failures stop.
func metadataMissing(err error) bool {
	var failure *authorizationStatus
	return errors.As(err, &failure) && failure.code == http.StatusNotFound
}

// authorizationFailure preserves cancellation and safe HTTP statuses. Decoder
// and network diagnostics are not wrapped because they can include token values,
// credential-bearing endpoint queries or sensitive issuer response content.
func authorizationFailure(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var failure *authorizationStatus
	if errors.As(err, &failure) {
		return fmt.Errorf("mcp: %s: %w", operation, failure)
	}
	return fmt.Errorf("mcp: %s failed transport, decoding or validation", operation)
}

// resourceMetadataAddresses derives endpoint-specific discovery followed by
// origin-root discovery. The endpoint address retains its exact query, which
// can distinguish resources at the same path; the root address has no query.
func resourceMetadataAddresses(resource *url.URL) []*url.URL {
	root := &url.URL{Scheme: resource.Scheme, Host: resource.Host, Path: "/.well-known/oauth-protected-resource"}
	if (resource.Path == "" || resource.Path == "/") && resource.RawQuery == "" && !resource.ForceQuery {
		return []*url.URL{root}
	}
	endpoint := *root
	if resource.Path != "/" {
		endpoint.Path += resource.Path
		endpoint.RawPath = root.Path + resource.EscapedPath()
	}
	endpoint.RawQuery = resource.RawQuery
	endpoint.ForceQuery = resource.ForceQuery
	return []*url.URL{&endpoint, root}
}

// issuerMetadataAddresses applies the protocol's OAuth and OpenID discovery
// order while retaining the configured issuer's escaped path exactly.
func issuerMetadataAddresses(issuer *url.URL) []*url.URL {
	oauth := &url.URL{Scheme: issuer.Scheme, Host: issuer.Host, Path: "/.well-known/oauth-authorization-server"}
	openid := &url.URL{Scheme: issuer.Scheme, Host: issuer.Host, Path: "/.well-known/openid-configuration"}
	if issuer.Path == "" || issuer.Path == "/" {
		return []*url.URL{oauth, openid}
	}
	oauth.Path += issuer.Path
	oauth.RawPath = "/.well-known/oauth-authorization-server" + issuer.EscapedPath()
	openid.Path += issuer.Path
	openid.RawPath = "/.well-known/openid-configuration" + issuer.EscapedPath()
	appended := *issuer
	appended.Path = strings.TrimSuffix(issuer.Path, "/") + "/.well-known/openid-configuration"
	appended.RawPath = strings.TrimSuffix(issuer.EscapedPath(), "/") + "/.well-known/openid-configuration"
	return []*url.URL{oauth, openid, &appended}
}
