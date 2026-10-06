// Use wraps the accepted-request handler with application HTTP middleware.
// Install middleware before requests begin; mounted routes use the same handler.
func (s *{{ .ServerStructDeclaration.Name }}) Use(m func(http.Handler) http.Handler) {
    s.handler = m(s.handler)
}

// ServeHTTP checks the request's MCP headers, metadata, method and origin before
// calling configured middleware. Direct and mounted requests follow this path.
func (s *{{ .ServerStructDeclaration.Name }}) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    s.processRequest(w, r)
}

// serveHTTP passes an accepted request through the current middleware handler.
// Reading the handler here retains middleware installed after mounting.
func (s *{{ .ServerStructDeclaration.Name }}) serveHTTP(w http.ResponseWriter, r *http.Request) {
    s.handler.ServeHTTP(w, r)
}
