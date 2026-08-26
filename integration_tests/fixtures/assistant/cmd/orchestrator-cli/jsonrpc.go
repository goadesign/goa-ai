package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	cli "example.com/assistant/gen/jsonrpc/cli/orchestrator"
	goahttp "goa.design/goa/v3/http"
)

func doJSONRPC(ctx context.Context, scheme, host string, timeout int, debug bool, stdout io.Writer) error {
	var (
		doer goahttp.Doer
	)
	{
		doer = &http.Client{Timeout: time.Duration(timeout) * time.Second}
		if debug {
			doer = goahttp.NewDebugDoer(doer)
		}
	}

	endpoint, payload, err := cli.ParseEndpoint(
		scheme,
		host,
		doer,
		goahttp.RequestEncoder,
		goahttp.ResponseDecoder,
		debug,
	)
	if err != nil {
		return fmt.Errorf("parse endpoint: %w", err)
	}

	switch flag.Arg(0) {
	case "mcp-assistant":
		switch flag.Arg(1) {
		case "initialize":
			return writeEndpointResult(ctx, stdout, endpoint, payload)
		case "notifications-initialized":
			return writeEndpointResult(ctx, stdout, endpoint, payload)
		case "ping":
			return writeEndpointResult(ctx, stdout, endpoint, payload)
		case "tools-list":
			return writeEndpointResult(ctx, stdout, endpoint, payload)
		case "tools-call":
			return writeEndpointResult(ctx, stdout, endpoint, payload)
		case "resources-list":
			return writeEndpointResult(ctx, stdout, endpoint, payload)
		case "resources-read":
			return writeEndpointResult(ctx, stdout, endpoint, payload)
		case "prompts-list":
			return writeEndpointResult(ctx, stdout, endpoint, payload)
		case "prompts-get":
			return writeEndpointResult(ctx, stdout, endpoint, payload)
		}
	}
	panic("parsed JSON-RPC command has no generated result writer")
}

func jsonrpcUsageExamples() string {
	return cli.UsageExamples()
}
