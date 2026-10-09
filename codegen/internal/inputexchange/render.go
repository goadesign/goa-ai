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

type (
	// questionRequestData supplies one statically selected wire representation.
	questionRequestData struct {
		*Question
		Native                 bool
		MCPPackage, RequestKey string
	}
)

//go:embed *.go.tpl
var sources embed.FS

// ContinuationSource writes host state and decoded answers into the native input.
func (input *Plan) ContinuationSource() (string, error) {
	return render("continuation.go.tpl", input)
}

// PendingSource validates native unfinished output and writes its host questions.
func (input *Plan) PendingSource() (string, error) {
	return render("pending.go.tpl", input)
}

// PendingSource writes a job's dynamically keyed questions using the shared
// form and URL request template. Its keys retain the native identity and kind.
func (plan *TaskPlan) PendingSource() (string, error) {
	return render("task_pending.go.tpl", plan)
}

// AnswerSource decodes host answers into the update method's native map. Unknown
// question kinds are ignored as allowed by the Tasks protocol; the service checks
// whether each submitted native identity is still outstanding for that job.
func (plan *TaskPlan) AnswerSource() (string, error) {
	return render("task_answer.go.tpl", plan)
}

// render specializes one conversion before the enclosing Go file is emitted.
func render(name string, data any) (string, error) {
	source, err := sources.ReadFile(name)
	if err != nil {
		return "", err
	}
	question, err := sources.ReadFile("question.go.tpl")
	if err != nil {
		return "", err
	}
	compiled, err := template.New(name).Funcs(template.FuncMap{
		"quote": strconv.Quote,
		"request": func(question *Question, native bool, mcpPackage, key string) questionRequestData {
			return questionRequestData{Question: question, Native: native, MCPPackage: mcpPackage, RequestKey: key}
		},
	}).Parse(string(question) + string(source))
	if err != nil {
		return "", err
	}
	var result bytes.Buffer
	if err := compiled.Execute(&result, data); err != nil {
		return "", err
	}
	return result.String(), nil
}
