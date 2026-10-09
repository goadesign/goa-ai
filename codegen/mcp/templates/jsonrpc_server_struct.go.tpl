{{ printf "%s handles guarded MCP requests through the configured Goa handlers." .ServerStructDeclaration.Name | comment }}
type {{ .ServerStructDeclaration.Name }} struct {
    {{- range .ConstructorDependencies }}
    {{ .Name }} {{ .TypeRef }}
    {{- end }}
    handler http.Handler
    // Methods is the list of protocol methods served by this server.
    Methods []string
    {{- range .Endpoints }}
    {{ printf "%s is the native Goa handler for the %s protocol method." .Method.VarName .Method.Name | comment }}
    {{ .Method.VarName }} func(context.Context, *http.Request, *jsonrpc.RawRequest, http.ResponseWriter) error
    {{- end }}

    decoder func(*http.Request) goahttp.Decoder
    encoder func(context.Context, http.ResponseWriter) goahttp.Encoder
    errhandler func(context.Context, http.ResponseWriter, error)
    // processRequest owns protocol checks before the mutable middleware handler.
    processRequest http.HandlerFunc
}
