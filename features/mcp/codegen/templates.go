package codegen

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
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
