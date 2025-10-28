package codegen

type (
	cliParseTemplateData struct {
		Services []cliServiceTemplateData
	}

	cliServiceTemplateData struct {
		Name    string
		Alias   string
		Methods []cliMethodTemplateData
	}

	cliMethodTemplateData struct {
		Command  string
		Endpoint string
	}
)
