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

	endpoint, payload, err := cli.ParseEndpoint(
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
	{{- range .Services }}
	case "{{ .Name }}":
		e := {{ .Alias }}.NewEndpoints(scheme, host, doer, goahttp.RequestEncoder, goahttp.ResponseDecoder, debug)
		switch subcmd {
		{{- range .Methods }}
		case "{{ .Command }}":
			return e.{{ .Endpoint }}, payload, nil
		{{- end }}
		}
		return endpoint, payload, nil
	{{- end }}
	}

	return endpoint, payload, nil
}
