// This command mounts the fixture's generated MCP service for the official
// protocol referee. It configures one localhost browser Origin and closes the
// listener on interruption. Example regeneration owns cmd/, so this command
// lives outside that directory and survives regeneration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	assistantapi "example.com/assistant"
	genmcpsrv "example.com/assistant/gen/jsonrpc/mcp_assistant/server"
	genmcp "example.com/assistant/gen/mcp_assistant"
	goahttp "goa.design/goa/v3/http"
)

func main() {
	port := flag.String("port", "31172", "Local port used by the protocol referee")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *port); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run mounts the generated service with an explicit localhost Origin. It returns
// listener or shutdown errors so the referee cannot mistake a failed server for
// a protocol result.
func run(ctx context.Context, port string) error {
	address := net.JoinHostPort("localhost", port)
	mux := goahttp.NewMuxer()
	endpoints := genmcp.NewEndpoints(assistantapi.NewMcpAssistant())
	handler := genmcpsrv.New(endpoints, mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, handleError, "http://"+address)
	genmcpsrv.Mount(mux, handler)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen for protocol referee: %w", err)
	}
	server := &http.Server{
		Handler: mux,
		// Bound the time one local connection may spend sending its HTTP headers.
		ReadHeaderTimeout: time.Minute,
	}
	finished := make(chan error, 1)
	go serve(server, listener, finished)
	select {
	case err := <-finished:
		return err
	case <-ctx.Done():
		if err := server.Close(); err != nil {
			return fmt.Errorf("close protocol referee server: %w", err)
		}
		return <-finished
	}
}

// serve owns the listener until the command stops and reports the actual server
// failure. Closing the server after an interrupt is a successful command exit.
func serve(server *http.Server, listener net.Listener, finished chan<- error) {
	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	finished <- err
}

// handleError reports a failed generated encoder or handler as an HTTP failure
// so the independent referee observes the failure instead of an empty success.
func handleError(_ context.Context, writer http.ResponseWriter, err error) {
	http.Error(writer, err.Error(), http.StatusInternalServerError)
}
