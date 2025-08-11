{{ comment "MountPoint holds information about the mounted endpoints." }}
type MountPoint struct {
	{{ comment "Method is the name of the service method served by the mounted HTTP handler." }}
	Method string
	{{ comment "Verb is the HTTP method used to match requests to the mounted handler." }}
	Verb string
	{{ comment "Pattern is the HTTP request path pattern used to match requests to the mounted handler." }}
	Pattern string
}

{{ comment "Server struct with Mounts field for goa example compatibility" }}
type Server struct {
	*jsonrpcServer
	{{ comment "Mounts lists the endpoints served by this server." }}
	Mounts []*MountPoint
}

{{ comment "jsonrpcServer is the underlying JSON-RPC server" }}
type jsonrpcServer = _Server


{{ comment "New creates a new MCP JSON-RPC server with Mounts field" }}
func New(e *{{ .ServiceName }}.Endpoints, mux goahttp.Muxer, decoder func(*http.Request) goahttp.Decoder, encoder func(context.Context, http.ResponseWriter) goahttp.Encoder, errhandler func(context.Context, http.ResponseWriter, error)) *Server {
	// Create the underlying JSON-RPC server
	srv := _New(e, mux, decoder, encoder, errhandler)
	
	// Wrap with Server that has Mounts
	s := &Server{
		jsonrpcServer: srv,
		Mounts: []*MountPoint{
			{{- range .Endpoints }}
			{"{{ .Name }}", "POST", "{{ $.JSONRPCPath }}"},
			{{- end }}
		},
	}
	
	return s
}

{{ comment "_New is the original New function renamed" }}
var _New = NewJSONRPCServer

{{ comment "Mount configures the mux to serve the MCP endpoints with correct path." }}
func Mount(mux goahttp.Muxer, s *Server) {
	// Mount at the correct JSONRPC path from design
	h := NewHandler(s.jsonrpcServer)
	mux.Handle("POST", "{{ .JSONRPCPath }}", h.ServeHTTP)
}