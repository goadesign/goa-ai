// Package inputexchange writes the same authored field conversion for MCP
// endpoints, local service executors and registry providers. Only the output
// representation is selected during generation; runtime code never selects a mode.
package inputexchange

import (
	"bytes"
	"embed"
	"strconv"
	"text/template"
)

//go:embed *.go.tpl
var sources embed.FS

// ContinuationSource writes host state and decoded answers into the native input.
func (input *Plan) ContinuationSource() (string, error) {
	return input.render("continuation.go.tpl")
}

// PendingSource validates native unfinished output and writes its host questions.
func (input *Plan) PendingSource() (string, error) {
	return input.render("pending.go.tpl")
}

// render specializes one conversion before the enclosing Go file is emitted.
func (input *Plan) render(name string) (string, error) {
	source, err := sources.ReadFile(name)
	if err != nil {
		return "", err
	}
	compiled, err := template.New(name).Funcs(template.FuncMap{"quote": strconv.Quote}).Parse(string(source))
	if err != nil {
		return "", err
	}
	var result bytes.Buffer
	if err := compiled.Execute(&result, input); err != nil {
		return "", err
	}
	return result.String(), nil
}
