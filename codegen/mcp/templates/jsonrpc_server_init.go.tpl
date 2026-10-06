{{ printf "%s constructs the MCP server with native Goa endpoint handlers and an origin policy." .ServerInitDeclaration.Name | comment }}
// An empty origins list rejects every request that supplies an Origin header.
// Both direct serving and mounting enforce this policy before configured middleware.
func {{ .ServerInitDeclaration.Name }}(
    endpoints *{{ .Service.PkgName }}.{{ .Service.EndpointsDeclaration.Name }},
    mux goahttp.Muxer,
    decoder func(*http.Request) goahttp.Decoder,
    encoder func(context.Context, http.ResponseWriter) goahttp.Encoder,
    errhandler func(context.Context, http.ResponseWriter, error),
    {{- range .ConstructorDependencies }}
    {{ .Name }} {{ .TypeRef }},
    {{- end }}
    origins ...string,
) *{{ .ServerStructDeclaration.Name }} {
    s := &{{ .ServerStructDeclaration.Name }}{
        {{- range .ConstructorDependencies }}
        {{ .Name }}: {{ .Name }},
        {{- end }}
        Methods: []string{
            {{- range .Endpoints }}
            {{ printf "%q" .Method.Name }},
            {{- end }}
        },
        {{- range .Endpoints }}
        {{ .Method.VarName }}: {{ .HandlerInit }}(endpoints.{{ .Method.VarName }}, mux, decoder, encoder, errhandler),
        {{- end }}
        decoder: decoder,
        encoder: encoder,
        errhandler: errhandler,
    }
    // The MCP protocol uses one ordinary request decoder. Subscriptions and
    // progress select their response stream inside the outer request guard.
    s.handler = http.HandlerFunc(s.handleHTTP)
    s.processRequest = withMCPTransport(s, origins, s.serveHTTP)
    return s
}
