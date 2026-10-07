// Package mcp protects MCP HTTP requests before configured middleware or service
// endpoints run. Applications construct the token verifier with trusted issuer
// settings; generated servers supply operation scope alternatives and mount the
// native Goa metadata handler. Rejections contain no token or issuer diagnostics.
package mcp

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	genmetadatasrv "goa.design/goa-ai/internal/mcpauth/gen/http/resource_metadata/server"
	genmetadata "goa.design/goa-ai/internal/mcpauth/gen/resource_metadata"
	goahttp "goa.design/goa/v3/http"
	"goa.design/goa/v3/security"
)

type (
	// JWTResource configures signed access tokens for one protected MCP resource.
	// The host supplies issuer-owned public keys and the registered algorithms.
	JWTResource struct {
		// Issuer is the exact HTTPS identifier accepted in the token's iss claim.
		Issuer string
		// Resource is the exact HTTPS resource identifier required in aud.
		Resource string
		// Keys contains trusted public signing keys supplied by the host.
		Keys jose.JSONWebKeySet
		// Algorithms contains the asymmetric signature algorithms accepted here.
		Algorithms []jose.SignatureAlgorithm
	}
	// ResourceServer verifies resource access before an MCP request is dispatched.
	// Generated transports provide scopes from the evaluated Goa security design.
	ResourceServer struct {
		verifier *jwtResourceVerifier
		metadata *url.URL
	}
	// ResourcePrincipal identifies the subject and client verified for a resource
	// request. Tool arguments cannot supply or replace these identity values.
	ResourcePrincipal struct {
		// Issuer is the authorization server that signed the accepted access token.
		Issuer string
		// Subject is the subject identified by that authorization server.
		Subject string
		// ClientID is the client to which the accepted access token was issued.
		ClientID string
	}
	// verifiedResourceGrant belongs to this verifier and this exact token. Native
	// Goa auth callbacks reuse its scope and identity checks only for the same
	// verifier, the same SHA-256 token hash and a still-valid signed interval.
	verifiedResourceGrant struct {
		owner       *ResourceServer
		fingerprint [sha256.Size]byte
		principal   ResourcePrincipal
		scopes      []string
		expires     float64
		notBefore   *float64
	}
	resourcePrincipalContextKey struct{}
	// resourceMetadataService returns only configured resource ownership facts.
	resourceMetadataService struct {
		resource, issuer string
		scopes           []string
	}
)

// NewJWTResourceServer constructs an RFC 9068 signed-access-token resource owner.
// It copies trusted public keys and rejects unsafe configuration. It accepts no
// ID tokens, client assertions, embedded token keys or token-selected key URLs.
func NewJWTResourceServer(config JWTResource) (*ResourceServer, error) {
	verifier, err := newJWTResourceVerifier(config.Issuer, config.Resource, config.Keys, config.Algorithms)
	if err != nil {
		return nil, err
	}
	resource, err := authorizationURL(config.Resource, false)
	if err != nil {
		return nil, err
	}
	return &ResourceServer{verifier: verifier, metadata: resourceMetadataAddresses(resource)[0]}, nil
}

// ResourcePrincipalFromContext returns the identity verified by the resource
// server or a native resource auth callback. Absence means no resource identity
// has been verified in this context; callers must not infer one from arguments.
func ResourcePrincipalFromContext(ctx context.Context) (ResourcePrincipal, bool) {
	grant, ok := ctx.Value(resourcePrincipalContextKey{}).(*verifiedResourceGrant)
	if !ok {
		return ResourcePrincipal{}, false
	}
	return grant.principal, true
}

// AuthorizeHTTP verifies the bearer token and accepts any complete scope
// alternative declared for this operation. A rejection writes its HTTP challenge
// and returns nil before middleware or domain work can run. Successful requests
// carry the verified identity in their context. Scope alternatives
// come from generated security expressions, never from model arguments.
func (s *ResourceServer) AuthorizeHTTP(writer http.ResponseWriter, request *http.Request, alternatives [][]string) *http.Request {
	ctx, span := otel.Tracer("goa-ai/mcp").Start(request.Context(), "mcp.oauth.resource.authorize")
	defer span.End()
	if request.Context().Err() != nil {
		span.AddEvent("request canceled before authorization")
		return nil
	}
	var challenged []string
	if len(alternatives) > 0 {
		challenged = alternatives[0]
	}
	// OAuth access tokens belong exclusively in the bearer header. Query names
	// are decoded to catch encoded access_token aliases without rewriting URLs.
	for _, segment := range strings.Split(request.URL.RawQuery, "&") {
		name, _, _ := strings.Cut(segment, "=")
		decoded, err := url.QueryUnescape(name)
		if err != nil || decoded == "access_token" {
			s.rejectHTTP(writer, http.StatusBadRequest, "invalid_request", challenged)
			span.AddEvent("access token query or malformed query name rejected")
			return nil
		}
	}
	values := request.Header.Values("Authorization")
	if len(values) == 0 {
		s.rejectHTTP(writer, http.StatusUnauthorized, "", challenged)
		span.AddEvent("bearer credential required")
		return nil
	}
	if len(values) != 1 {
		s.rejectHTTP(writer, http.StatusBadRequest, "invalid_request", challenged)
		span.AddEvent("multiple authorization headers rejected")
		return nil
	}
	scheme, token, found := strings.Cut(values[0], " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		s.rejectHTTP(writer, http.StatusUnauthorized, "", challenged)
		span.AddEvent("bearer credential required")
		return nil
	}
	// RFC 6750 permits one or more spaces after the scheme. Spaces within a
	// compact signed token remain invalid and fail signature parsing.
	token = strings.TrimLeft(token, " ")
	grant, err := s.verifyGrant(ctx, token)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			span.AddEvent("request canceled during token verification")
			return nil
		}
		s.rejectHTTP(writer, http.StatusUnauthorized, "invalid_token", challenged)
		span.AddEvent("access token rejected")
		return nil
	}
	if !resourceScopesSatisfied(alternatives, grant.scopes) {
		s.rejectHTTP(writer, http.StatusForbidden, oauthInsufficientScope, challenged)
		span.AddEvent("operation scopes required")
		return nil
	}
	span.SetAttributes(attribute.Bool("oauth.resource.authorized", true))
	span.AddEvent("resource request authorized")
	return request.WithContext(context.WithValue(request.Context(), resourcePrincipalContextKey{}, grant))
}

// OAuth2Auth implements Goa's OAuth authentication signature. A verified MCP
// request reuses its exact token grant; ordinary Goa callers verify the token
// here. Goa's own scope validator checks the original method's requirements.
func (s *ResourceServer) OAuth2Auth(ctx context.Context, token string, scheme *security.OAuth2Scheme) (context.Context, error) {
	return s.authenticate(ctx, token, scheme.Validate)
}

// JWTAuth implements Goa's JWT authentication signature for resource access
// tokens. It preserves the selected method's native scope requirements and
// returns a context containing the verified subject and client identity.
func (s *ResourceServer) JWTAuth(ctx context.Context, token string, scheme *security.JWTScheme) (context.Context, error) {
	return s.authenticate(ctx, token, scheme.Validate)
}

// BearerAuth implements Goa's Bearer authentication signature for resource
// access tokens. Goa checks method scopes after the configured resource owner
// verifies the token and adds its identity to the returned context.
func (s *ResourceServer) BearerAuth(ctx context.Context, token string, scheme *security.BearerScheme) (context.Context, error) {
	return s.authenticate(ctx, token, scheme.Validate)
}

// MountMetadata registers the generated Goa metadata handler at this resource's
// well-known path. Generated servers supply their basic-access scopes. Metadata
// is public and never invokes MCP middleware, authentication or a service tool.
func (s *ResourceServer) MountMetadata(mux goahttp.Muxer, scopes []string) {
	service := &resourceMetadataService{
		resource: s.verifier.resource,
		issuer:   s.verifier.issuer,
		scopes:   slices.Clone(scopes),
	}
	server := genmetadatasrv.New(genmetadata.NewEndpoints(service), mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, nil, nil)
	mux.Handle(http.MethodGet, s.metadata.EscapedPath(), func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.RawQuery != s.metadata.RawQuery || request.URL.ForceQuery != s.metadata.ForceQuery {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		server.Read.ServeHTTP(writer, request)
	})
}

// Read returns fresh metadata slices for one HTTP request. Callers cannot
// mutate the scopes or issuer retained by this mounted resource handler.
func (s *resourceMetadataService) Read(context.Context) (*genmetadata.ReadResult, error) {
	return &genmetadata.ReadResult{
		Resource: s.resource, AuthorizationServers: []string{s.issuer},
		ScopesSupported: slices.Clone(s.scopes),
	}, nil
}

// resourceScopesSatisfied checks each generated alternative independently.
// No required scopes means a valid resource token is enough; scope names are
// exact and a token must contain every scope in one alternative.
func resourceScopesSatisfied(alternatives [][]string, granted []string) bool {
	if len(alternatives) == 0 {
		return true
	}
	for _, required := range alternatives {
		complete := true
		for _, scope := range required {
			if !slices.Contains(granted, scope) {
				complete = false
				break
			}
		}
		if complete {
			return true
		}
	}
	return false
}

// authenticate accepts only a grant for this verifier and the exact supplied
// token. Direct Goa calls have no HTTP grant, so they verify the token first.
// A scope rejection returns the original context and never creates a principal.
func (s *ResourceServer) authenticate(ctx context.Context, token string, validate func([]string) error) (authenticated context.Context, err error) {
	authenticated = ctx
	ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.oauth.resource.method_auth")
	defer span.End()
	defer func() {
		if err != nil {
			failure := errors.New("mcp: native resource authentication rejected")
			span.RecordError(failure)
			span.SetStatus(codes.Error, failure.Error())
		}
	}()
	if err := ctx.Err(); err != nil {
		return authenticated, err
	}
	grant, ok := ctx.Value(resourcePrincipalContextKey{}).(*verifiedResourceGrant)
	if !ok || grant.owner != s || grant.fingerprint != sha256.Sum256([]byte(token)) {
		var err error
		grant, err = s.verifyGrant(ctx, token)
		if err != nil {
			return authenticated, err
		}
	}
	if !resourceTokenTimeValid(time.Now(), grant.expires, grant.notBefore) {
		return authenticated, errInvalidAccessToken
	}
	if err := validate(grant.scopes); err != nil {
		return authenticated, err
	}
	span.AddEvent("native resource authentication accepted")
	return context.WithValue(authenticated, resourcePrincipalContextKey{}, grant), nil
}

// verifyGrant validates a token once and retains only the identity, scopes and
// token fingerprint needed by native auth callbacks. Raw tokens and unrelated
// signed claims do not enter the authenticated request context.
func (s *ResourceServer) verifyGrant(ctx context.Context, token string) (*verifiedResourceGrant, error) {
	claims, err := s.verifier.verify(ctx, token, time.Now())
	if err != nil {
		return nil, err
	}
	grant := &verifiedResourceGrant{
		owner: s, fingerprint: sha256.Sum256([]byte(token)),
		principal: ResourcePrincipal{Issuer: claims.Iss, Subject: claims.Sub, ClientID: claims.ClientID},
		expires:   claims.Exp, notBefore: claims.Nbf,
	}
	if claims.Scope != nil {
		grant.scopes = strings.Split(*claims.Scope, " ")
	}
	return grant, nil
}

// rejectHTTP writes a challenge for checks performed before request dispatch.
// Clients receive the exact metadata address and one complete required scope
// alternative; failures returned later by a domain endpoint never call this.
func (s *ResourceServer) rejectHTTP(writer http.ResponseWriter, status int, code string, scopes []string) {
	challenge := "Bearer resource_metadata=" + strconv.Quote(s.metadata.String())
	if code != "" {
		challenge += ", error=" + strconv.Quote(code)
	}
	if len(scopes) > 0 {
		challenge += ", scope=" + strconv.Quote(strings.Join(scopes, " "))
	}
	writer.Header().Set("WWW-Authenticate", challenge)
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
}
