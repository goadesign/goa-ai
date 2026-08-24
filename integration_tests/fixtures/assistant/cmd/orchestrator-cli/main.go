package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	goa "goa.design/goa/v3/pkg"
)

func main() {
	var (
		hostF = flag.String("host", "dev", "Server host (valid values: dev)")
		addrF = flag.String("url", "", "URL to service host")

		verboseF = flag.Bool("verbose", false, "Print request and response details")
		vF       = flag.Bool("v", false, "Print request and response details")
		timeoutF = flag.Int("timeout", 30, "Maximum number of seconds to wait for response")
	)
	flag.Usage = usage
	flag.Parse()

	var (
		addr    string
		timeout int
		debug   bool
	)
	{
		addr = *addrF
		if addr == "" {
			switch *hostF {
			case "dev":
				addr = "http://localhost:8080"
			default:
				fmt.Fprintf(os.Stderr, "invalid host argument: %q (valid hosts: dev)\n", *hostF)
				os.Exit(1)
			}
		}
		timeout = *timeoutF
		debug = *verboseF || *vF
	}

	var (
		scheme string
		host   string
	)
	{
		u, err := url.Parse(addr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid URL %#v: %s\n", addr, err)
			os.Exit(1)
		}
		scheme = u.Scheme
		host = u.Host
	}

	var (
		err error
	)
	{
		switch scheme {
		case "http", "https":
			err = doJSONRPC(context.Background(), scheme, host, timeout, debug, os.Stdout)
		default:
			fmt.Fprintf(os.Stderr, "invalid scheme: %q (valid schemes: http)\n", scheme)
			os.Exit(1)
		}
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, err.Error())
		fmt.Fprintln(os.Stderr, "run '"+os.Args[0]+" --help' for detailed usage.")
		os.Exit(1)
	}

}

// writeEndpointResult calls one normal endpoint and writes its result as JSON.
func writeEndpointResult(ctx context.Context, stdout io.Writer, endpoint goa.Endpoint, payload any) error {
	data, err := endpoint(ctx, payload)
	if err != nil {
		return err
	}
	return writeJSON(stdout, data)
}

// writeStreamResults writes each server result until the server ends the stream.
func writeStreamResults[T any](ctx context.Context, stdout io.Writer, recv func(context.Context) (T, error)) error {
	for {
		data, err := recv(ctx)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("receive result: %w", err)
		}
		if err := writeJSON(stdout, data); err != nil {
			return err
		}
	}
}

// writeJSON writes one indented JSON value followed by a newline.
func writeJSON(stdout io.Writer, data any) error {
	if data == nil {
		return nil
	}
	encoded, err := json.MarshalIndent(data, "", "    ")
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	if _, err := fmt.Fprintln(stdout, string(encoded)); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	return nil
}

func usage() {
	usageCommands := []string{
		"mcp-assistant (initialize|ping|tools-list|tools-call|resources-list|resources-read|resources-subscribe|resources-unsubscribe|prompts-list|prompts-get|notify-status-update|events-stream)",
	}
	fmt.Fprintf(os.Stderr, `%s is a command line client for the assistant API.

Usage:
    %s [-host HOST][-url URL][-timeout SECONDS][-verbose|-v] SERVICE ENDPOINT [flags]

    -host HOST:  server host (dev). valid values: dev
    -url URL:    specify service URL overriding host URL (http://localhost:8080)
    -timeout:    maximum number of seconds to wait for response (30)
    -verbose|-v: print request and response details (false)

Commands:
%s
Additional help:
    %s SERVICE [ENDPOINT] --help

Example:
%s
`, os.Args[0], os.Args[0], indent(strings.Join(usageCommands, "\n")), os.Args[0], indent(jsonrpcUsageExamples()))
}

func indent(s string) string {
	if s == "" {
		return ""
	}
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}
