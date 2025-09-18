package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"

	assistantapi "example.com/assistant"
	assistant "example.com/assistant/gen/assistant"
	grpcstream "example.com/assistant/gen/grpcstream"
	mcpassistant "example.com/assistant/gen/mcp_assistant"
	streaming "example.com/assistant/gen/streaming"
	websocketsvc "example.com/assistant/gen/websocket"
	"goa.design/clue/debug"
	"goa.design/clue/log"
)

func main() {
	// Define command line flags, add any other flag required to configure the
	// service.
	var (
		hostF     = flag.String("host", "localhost", "Server host (valid values: localhost)")
		domainF   = flag.String("domain", "", "Host domain name (overrides host domain specified in service design)")
		grpcPortF = flag.String("grpc-port", "", "gRPC port (overrides host gRPC port specified in service design)")
		httpPortF = flag.String("http-port", "", "HTTP port (overrides host HTTP port specified in service design)")
		secureF   = flag.Bool("secure", false, "Use secure scheme (https or grpcs)")
		dbgF      = flag.Bool("debug", false, "Log request and response bodies")
	)
	flag.Parse()

	// Setup logger. Replace logger with your own log package of choice.
	format := log.FormatJSON
	if log.IsTerminal() {
		format = log.FormatTerminal
	}
	ctx := log.Context(context.Background(), log.WithFormat(format))
	if *dbgF {
		ctx = log.Context(ctx, log.WithDebug())
		log.Debugf(ctx, "debug logs enabled")
	}
	log.Print(ctx, log.KV{K: "http-port", V: *httpPortF})

	// Initialize the services.
	var (
		grpcstreamSvc   grpcstream.Service
		websocketSvc    websocketsvc.Service
		streamingSvc    streaming.Service
		assistantSvc    assistant.Service
		mcpAssistantSvc mcpassistant.Service
	)
	{
		grpcstreamSvc = assistantapi.NewGrpcstream()
		websocketSvc = assistantapi.NewWebsocket()
		streamingSvc = assistantapi.NewStreaming()
		assistantSvc = assistantapi.NewAssistant()
		mcpAssistantSvc = assistantapi.NewMcpAssistant()
	}

	{
		// Wire MCP adapters on top of original services
		// Provide a simple prompt provider implementation so Prompts.get works.
		provider := assistantapi.NewPromptProvider()
		mcpAssistantSvc = mcpassistant.NewMCPAdapter(assistantSvc, provider, nil)
	}

	// Wrap the services in endpoints that can be invoked from other services
	// potentially running in different processes.
	var (
		grpcstreamEndpoints   *grpcstream.Endpoints
		websocketEndpoints    *websocketsvc.Endpoints
		streamingEndpoints    *streaming.Endpoints
		assistantEndpoints    *assistant.Endpoints
		mcpAssistantEndpoints *mcpassistant.Endpoints
	)
	{
		grpcstreamEndpoints = grpcstream.NewEndpoints(grpcstreamSvc)
		grpcstreamEndpoints.Use(debug.LogPayloads())
		grpcstreamEndpoints.Use(log.Endpoint)
		websocketEndpoints = websocketsvc.NewEndpoints(websocketSvc)
		websocketEndpoints.Use(debug.LogPayloads())
		websocketEndpoints.Use(log.Endpoint)
		streamingEndpoints = streaming.NewEndpoints(streamingSvc)
		streamingEndpoints.Use(debug.LogPayloads())
		streamingEndpoints.Use(log.Endpoint)
		assistantEndpoints = assistant.NewEndpoints(assistantSvc)
		assistantEndpoints.Use(debug.LogPayloads())
		assistantEndpoints.Use(log.Endpoint)
		mcpAssistantEndpoints = mcpassistant.NewEndpoints(mcpAssistantSvc)
		mcpAssistantEndpoints.Use(debug.LogPayloads())
		mcpAssistantEndpoints.Use(log.Endpoint)
	}

	// Create channel used by both the signal handler and server goroutines
	// to notify the main goroutine when to stop the server.
	errc := make(chan error)

	// Setup interrupt handler. This optional step configures the process so
	// that SIGINT and SIGTERM signals cause the services to stop gracefully.
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
		errc <- fmt.Errorf("%s", <-c)
	}()

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(ctx)

	// Start the servers and send errors (if any) to the error channel.
	switch *hostF {
	case "localhost":
		{
			addr := "http://localhost:80"
			u, err := url.Parse(addr)
			if err != nil {
				log.Fatalf(ctx, err, "invalid URL %#v\n", addr)
			}
			if *secureF {
				u.Scheme = "https"
			}
			if *domainF != "" {
				u.Host = *domainF
			}
			if *httpPortF != "" {
				h, _, err := net.SplitHostPort(u.Host)
				if err != nil {
					log.Fatalf(ctx, err, "invalid URL %#v\n", u.Host)
				}
				u.Host = net.JoinHostPort(h, *httpPortF)
			} else if u.Port() == "" {
				u.Host = net.JoinHostPort(u.Host, "80")
			}
			handleHTTPServer(ctx, u, websocketEndpoints, streamingEndpoints, mcpAssistantEndpoints, mcpAssistantSvc, &wg, errc, *dbgF)
		}

		{
			addr := "grpc://localhost:8080"
			u, err := url.Parse(addr)
			if err != nil {
				log.Fatalf(ctx, err, "invalid URL %#v\n", addr)
			}
			if *secureF {
				u.Scheme = "grpcs"
			}
			if *domainF != "" {
				u.Host = *domainF
			}
			if *grpcPortF != "" {
				h, _, err := net.SplitHostPort(u.Host)
				if err != nil {
					log.Fatalf(ctx, err, "invalid URL %#v\n", u.Host)
				}
				u.Host = net.JoinHostPort(h, *grpcPortF)
			} else if u.Port() == "" {
				u.Host = net.JoinHostPort(u.Host, "8080")
			}
			handleGRPCServer(ctx, u, grpcstreamEndpoints, &wg, errc, *dbgF)
		}

	default:
		log.Fatal(ctx, fmt.Errorf("invalid host argument: %q (valid hosts: localhost)", *hostF))
	}

	// Wait for signal.
	log.Printf(ctx, "exiting (%v)", <-errc)

	// Send cancellation signal to the goroutines.
	cancel()

	wg.Wait()
	log.Printf(ctx, "exited")
}
