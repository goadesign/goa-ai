// This command serves generated MCP endpoints for the browser example. It
// loads the already-built panel at startup and owns only domain method behavior.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"

	genserver "apps-peer.local/gen/jsonrpc/mcp_records/server"
	genmcp "apps-peer.local/gen/mcp_records"
	genservice "apps-peer.local/gen/records"
	goahttp "goa.design/goa/v3/http"
)

type (
	recordsService struct {
		panel    string
		evidence string
	}
)

func main() {
	if err := run(); err != nil {
		if _, writeErr := fmt.Fprintln(os.Stderr, err); writeErr != nil {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

// run loads the configured HTML, builds the generated endpoints with one
// allowed browser origin, and closes the listener when its parent stops it.
func run() error {
	address := flag.String("listen", "127.0.0.1:43173", "Synthetic MCP listener")
	hostOrigin := flag.String("host-origin", "http://127.0.0.1:43170", "Exact browser origin allowed to call MCP")
	panelFile := flag.String("panel", "../.cache/panel.html", "Built HTML panel")
	evidence := flag.String("evidence", "../.cache", "Private directory for synthetic cancellation evidence")
	flag.Parse()
	panel, err := os.ReadFile(*panelFile)
	if err != nil {
		return fmt.Errorf("read panel: %w", err)
	}
	service := &recordsService{panel: string(panel), evidence: *evidence}
	adapter := genmcp.NewMCPAdapter(genservice.NewEndpoints(service), nil)
	mux := goahttp.NewMuxer()
	server := genserver.New(genmcp.NewEndpoints(adapter), mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, nil, *hostOrigin)
	genserver.Mount(mux, server)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	// #nosec G112 -- The loopback acceptance fixture is stopped by its test runner, not deployed as a public listener.
	httpServer := &http.Server{Addr: *address, Handler: mux}
	stopped := make(chan error, 1)
	go shutdown(ctx, httpServer, stopped)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		stop()
		return err
	}
	return <-stopped
}

// Panel returns the HTML loaded by the command before any requests began.
func (s *recordsService) Panel(context.Context) (string, error) {
	return s.panel, nil
}

// Show returns the model summary and the separately encoded app identity.
func (s *recordsService) Show(context.Context) (*genservice.ShowResult, error) {
	return &genservice.ShowResult{
		Summary:       "record",
		PrivateRecord: &genservice.PrivateRecord{RecordID: "record-1"},
	}, nil
}

// Refresh returns the updated summary after an app-visible call is accepted.
func (s *recordsService) Refresh(context.Context) (*genservice.RefreshResult, error) {
	return &genservice.RefreshResult{Summary: "refreshed"}, nil
}

// Query returns a synthetic result only when the host permits the model call.
func (s *recordsService) Query(context.Context) (string, error) {
	return "query", nil
}

// Wait records entry into the generated endpoint, then records when its HTTP
// context is canceled. The browser test reads both files to verify cancellation.
func (s *recordsService) Wait(ctx context.Context) (string, error) {
	if err := os.WriteFile(filepath.Join(s.evidence, "wait_started"), []byte("started"), 0600); err != nil {
		return "", err
	}
	<-ctx.Done()
	if err := os.WriteFile(filepath.Join(s.evidence, "wait_canceled"), []byte("canceled"), 0600); err != nil {
		return "", err
	}
	return "", ctx.Err()
}

// shutdown waits for the command's stop signal and reports listener cleanup
// to run so an error cannot disappear in a detached goroutine.
func shutdown(ctx context.Context, server *http.Server, stopped chan<- error) {
	<-ctx.Done()
	stopped <- server.Shutdown(context.Background())
}
