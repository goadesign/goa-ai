package codegen

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"text/template"
)

// Types
type (
	// templates reads templates and partials from a provided filesystem.
	templates struct {
		FS fs.FS
	}
)

//go:embed templates/*.go.tpl
var templateFS embed.FS

// mcpTemplates is the single template reader used across the codegen package
var mcpTemplates = &templates{FS: templateFS}

// Read returns the template with the given name.
func (tr *templates) Read(name string) string {
	content, err := fs.ReadFile(tr.FS, path.Join("templates", name+".go.tpl"))
	if err != nil {
		panic(fmt.Sprintf("failed to load template %s: %v", name, err))
	}
	return string(content)
}

// MustRender applies the template with the provided data and returns the rendered string.
// The template is expected to be a Go text/template stored under templates/<name>.go.tpl.
func (tr *templates) MustRender(name string, data any) string {
	const tmplName = "mcp-template"
	content := tr.Read(name)
	tmpl, err := template.New(tmplName).Parse(content)
	if err != nil {
		panic(fmt.Sprintf("failed to parse template %s: %v", name, err))
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		panic(fmt.Sprintf("failed to render template %s: %v", name, err))
	}
	return buf.String()
}
