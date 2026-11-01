// Package prompt implements template parsing and static AST field validation for
// prompt templates.
package prompt

import (
	"fmt"
	"text/template"

	"goa.design/goa-ai/internal/tmplcheck"
)

// ValidatePromptTemplate parses templateSource and verifies field accesses
// against spec.ArgsType using the template AST.
func ValidatePromptTemplate(spec PromptSpec, templateSource string) error {
	tmpl := template.New(spec.ID.String()).Option("missingkey=error")
	funcs := template.FuncMap{
		"render": func(string, ...any) (string, error) {
			return "", nil
		},
	}
	for k, v := range spec.Funcs {
		funcs[k] = v
	}
	tmpl = tmpl.Funcs(funcs)

	parsed, err := tmpl.Parse(templateSource)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrTemplateParse, spec.ID, err)
	}

	if spec.ArgsType != nil {
		if err := tmplcheck.Check(parsed, spec.ID.String(), tmplcheck.ReflectType(spec.ArgsType), funcs); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrTemplateFieldNotFound, spec.ID, err)
		}
	}

	return nil
}
