{{ comment "ParseEndpoint returns the endpoint and payload as specified on the command line." }}
func ParseEndpoint(scheme, host string, doer goahttp.Doer, enc func(*http.Request) goahttp.Encoder, dec func(*http.Response) goahttp.Decoder, restore bool) (goa.Endpoint, any, error) {
	var (
		assistantFlags = flag.NewFlagSet("assistant", flag.ContinueOnError)
		{{- range .Endpoints }}
		{{ .VarName }}Body string
		{{- end }}
		
		body string
	)
	assistantFlags.Usage = assistantUsage
	assistantFlags.StringVar(&body, "body", "", "JSON-RPC request body")
	
	// After main flags are parsed, remaining args are in flag.Args()
	args := flag.Args()
	if len(args) < 2 {
		return nil, nil, fmt.Errorf("missing service and endpoint arguments")
	}
	
	// Skip service name (args[0]) and parse from endpoint name (args[1]) onward
	if err := assistantFlags.Parse(args[1:]); err != nil {
		return nil, nil, err
	}
	
	endpoint := assistantFlags.Arg(0)
	
	switch endpoint {
	{{- range .Endpoints }}
	case "{{ .Name }}":
		assistantFlags.StringVar(&{{ .VarName }}Body, "{{ .FlagName }}-body", "", "{{ .Name }} endpoint body")
		if err := assistantFlags.Parse(args[2:]); err != nil {
			return nil, nil, err
		}
		if {{ .VarName }}Body == "" {
			{{ .VarName }}Body = body
		}
		if {{ .VarName }}Body == "" {
			return nil, nil, fmt.Errorf("--{{ .FlagName }}-body or --body flag is required")
		}
		{{- if .HasPayload }}
		payload, err := {{ .BuildFunc }}({{ .VarName }}Body)
		if err != nil {
			return nil, nil, err
		}
		endpoint := NewClient(scheme, host, doer, enc, dec, restore).{{ .MethodName }}()
		return endpoint, payload, nil
		{{- else }}
		// {{ .MethodName }} has no payload
		endpoint := NewClient(scheme, host, doer, enc, dec, restore).{{ .MethodName }}()
		return endpoint, nil, nil
		{{- end }}
	{{- end }}
	default:
		return nil, nil, fmt.Errorf("unknown endpoint: %s", endpoint)
	}
}

{{ comment "UsageCommands returns the set of commands and sub-commands using the format" }}
{{ comment "" }}
{{ comment "\tcommand (subcommand1|subcommand2|...)" }}
func UsageCommands() string {
	return `assistant ({{ range $i, $e := .Endpoints }}{{ if $i }}|{{ end }}{{ .Name }}{{ end }})`
}

{{ comment "UsageExamples produces an example of a valid invocation of the CLI tool." }}
func UsageExamples() string {
	return `os.Args[0] assistant {{ (index .Endpoints 0).Name }} --body '{{ .ExampleJSON }}'`
}

func assistantUsage() {
	fmt.Fprintf(os.Stderr, `Usage:\n    %s assistant COMMAND [flags]\n\nCOMMANDS:\n`, os.Args[0])
	{{- range .Endpoints }}
	fmt.Fprintf(os.Stderr, "    {{ .Name }}\\n")
	{{- end }}
	fmt.Fprintf(os.Stderr, "\\nFLAGS:\\n")
	flag.PrintDefaults()
}