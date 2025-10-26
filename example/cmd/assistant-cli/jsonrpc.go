package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	cli "example.com/assistant/gen/jsonrpc/cli/assistant"
	mcpac "example.com/assistant/gen/mcp_assistant/adapter/client"
	goahttp "goa.design/goa/v3/http"
	goa "goa.design/goa/v3/pkg"
)

func doJSONRPC(scheme, host string, timeout int, debug bool) (goa.Endpoint, any, error) {
	var (
		doer goahttp.Doer
	)
	{
		doer = &http.Client{Timeout: time.Duration(timeout) * time.Second}
		if debug {
			doer = goahttp.NewDebugDoer(doer)
		}
	}

	_, payload, err := cli.ParseEndpoint(
		scheme,
		host,
		doer,
		goahttp.RequestEncoder,
		goahttp.ResponseDecoder,
		debug,
	)
	if err != nil {
		return nil, nil, err
	}
	// MCP-backed adapter endpoints
	e := mcpac.NewEndpoints(scheme, host, doer, goahttp.RequestEncoder, goahttp.ResponseDecoder, debug)
	// Extract non-flag args for service and subcommand
	var nonflags []string
	for i := 1; i < len(os.Args); i++ {
		a := os.Args[i]
		if strings.HasPrefix(a, "-") {
			if !strings.Contains(a, "=") && i+1 < len(os.Args) {
				i++
			}
			continue
		}
		nonflags = append(nonflags, a)
	}
	if len(nonflags) < 2 {
		return nil, nil, fmt.Errorf("not enough arguments")
	}
	service := nonflags[0]
	subcmd := nonflags[1]
	switch service {
	default:
		switch subcmd {
		case "analyze-text":
			return e.AnalyzeText, payload, nil
		case "search-knowledge":
			return e.SearchKnowledge, payload, nil
		case "execute-code":
			return e.ExecuteCode, payload, nil
		case "list-documents":
			return e.ListDocuments, payload, nil
		case "get-system-info":
			return e.GetSystemInfo, payload, nil
		case "generate-prompts":
			return e.GeneratePrompts, payload, nil
		case "send-notification":
			return e.SendNotification, payload, nil
		case "subscribe-to-updates":
			return e.SubscribeToUpdates, payload, nil
		case "process-batch":
			return e.ProcessBatch, payload, nil
		}
	}
	return nil, nil, fmt.Errorf("unknown service %q or command %q", service, subcmd)
}

func jsonrpcUsageCommands() []string {
	return cli.UsageCommands()
}

func jsonrpcUsageExamples() string {
	return cli.UsageExamples()
}
