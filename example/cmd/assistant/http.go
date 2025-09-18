package main

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"time"

	streamingsvr "example.com/assistant/gen/http/streaming/server"
	websocketsvcsvr "example.com/assistant/gen/http/websocket/server"
	mcpassistantjssvr "example.com/assistant/gen/jsonrpc/mcp_assistant/server"
	mcpassistant "example.com/assistant/gen/mcp_assistant"
	streaming "example.com/assistant/gen/streaming"
	websocketsvc "example.com/assistant/gen/websocket"
	"github.com/gorilla/websocket"
	"goa.design/clue/debug"
	"goa.design/clue/log"
	goahttp "goa.design/goa/v3/http"
)

func handleHTTPServer(ctx context.Context, u *url.URL, websocketEndpoints *websocketsvc.Endpoints, streamingEndpoints *streaming.Endpoints, mcpAssistantEndpoints *mcpassistant.Endpoints, mcpAssistantSvc mcpassistant.Service, wg *sync.WaitGroup, errc chan error, dbg bool) {

	// Provide the transport specific request decoder and response encoder.
	// The goa http package has built-in support for JSON, XML and gob.
	// Other encodings can be used by providing the corresponding functions,
	// see goa.design/implement/encoding.
	var (
		dec = goahttp.RequestDecoder
		enc = goahttp.ResponseEncoder
	)

	// Build the service HTTP request multiplexer and mount debug and profiler
	// endpoints in debug mode.
	var mux goahttp.Muxer
	{
		mux = goahttp.NewMuxer()
		if dbg {
			// Mount pprof handlers for memory profiling under /debug/pprof.
			debug.MountPprofHandlers(debug.Adapt(mux))
			// Mount /debug endpoint to enable or disable debug logs at runtime.
			debug.MountDebugLogEnabler(debug.Adapt(mux))
		}
	}

	// Wrap the endpoints with the transport specific layers. The generated
	// server packages contains code generated from the design which maps
	// the service input and output data structures to HTTP requests and
	// responses.
	var (
		websocketServer           *websocketsvcsvr.Server
		streamingServer           *streamingsvr.Server
		mcpAssistantJSONRPCServer *mcpassistantjssvr.Server
	)
	{
		eh := errorHandler(ctx)
		upgrader := &websocket.Upgrader{}
		websocketServer = websocketsvcsvr.New(websocketEndpoints, mux, dec, enc, eh, nil, upgrader, nil)
		streamingServer = streamingsvr.New(streamingEndpoints, mux, dec, enc, eh, nil, upgrader, nil)
		mcpAssistantJSONRPCServer = mcpassistantjssvr.New(mcpAssistantEndpoints, mux, dec, enc, eh)
	}

	// Configure the mux.
	websocketsvcsvr.Mount(mux, websocketServer)
	streamingsvr.Mount(mux, streamingServer)
	mcpassistantjssvr.Mount(mux, mcpAssistantJSONRPCServer)

	var handler http.Handler = mux
	if dbg {
		// Log query and response bodies if debug logs are enabled.
		handler = debug.HTTP()(handler)
	}
	handler = log.HTTP(ctx)(handler)

	// Start HTTP server using default configuration, change the code to
	// configure the server as required by your service.
	srv := &http.Server{Addr: u.Host, Handler: handler, ReadHeaderTimeout: time.Second * 60}
	for _, m := range websocketServer.Mounts {
		log.Printf(ctx, "HTTP %q mounted on %s %s", m.Method, m.Verb, m.Pattern)
	}
	for _, m := range streamingServer.Mounts {
		log.Printf(ctx, "HTTP %q mounted on %s %s", m.Method, m.Verb, m.Pattern)
	}
	for _, m := range mcpAssistantJSONRPCServer.Methods {
		log.Printf(ctx, "JSON-RPC method %q mounted on POST /rpc", m)
	}

	(*wg).Add(1)
	go func() {
		defer (*wg).Done()

		// Start HTTP server in a separate goroutine.
		go func() {
			log.Printf(ctx, "HTTP server listening on %q", u.Host)
			errc <- srv.ListenAndServe()
		}()

		<-ctx.Done()
		log.Printf(ctx, "shutting down HTTP server at %q", u.Host)

		// Shutdown gracefully with a 30s timeout.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err := srv.Shutdown(ctx)
		if err != nil {
			log.Printf(ctx, "failed to shutdown: %v", err)
		}
	}()
}

// errorHandler returns a function that writes and logs the given error.
// The function also writes and logs the error unique ID so that it's possible
// to correlate.
func errorHandler(logCtx context.Context) func(context.Context, http.ResponseWriter, error) {
	return func(ctx context.Context, w http.ResponseWriter, err error) {
		log.Printf(logCtx, "ERROR: %s", err.Error())
	}
}
