// Package hints provides lightweight, template-based rendering helpers for
// tool call/result hints. It exposes a global registry usable by sinks without
// requiring a runtime instance.
package hints

import (
	"fmt"
	"strings"
	"sync"
	"text/template"

	"goa.design/goa-ai/runtime/agent/tools"
)

var (
	mu          sync.RWMutex
	callHints   = make(map[tools.Ident]*template.Template)
	resultHints = make(map[tools.Ident]*template.Template)
)

// RegisterCallHint registers a compiled template for a tool call hint.
func RegisterCallHint(id tools.Ident, tmpl *template.Template) {
	if id == "" || tmpl == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	callHints[id] = tmpl
}

// RegisterResultHint registers a compiled template for a tool result hint.
func RegisterResultHint(id tools.Ident, tmpl *template.Template) {
	if id == "" || tmpl == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	resultHints[id] = tmpl
}

// RegisterCallHints registers multiple call hint templates.
func RegisterCallHints(m map[tools.Ident]*template.Template) {
	if len(m) == 0 {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	for k, v := range m {
		if k != "" && v != nil {
			callHints[k] = v
		}
	}
}

// RegisterResultHints registers multiple result hint templates.
func RegisterResultHints(m map[tools.Ident]*template.Template) {
	if len(m) == 0 {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	for k, v := range m {
		if k != "" && v != nil {
			resultHints[k] = v
		}
	}
}

// FormatCallHint renders the call hint for the given tool and payload. Returns
// an empty string when no template is registered or rendering fails.
func FormatCallHint(id tools.Ident, payload any) string {
	mu.RLock()
	tmpl := callHints[id]
	if tmpl == nil {
		tmpl = callHints[tools.Ident(id.Tool())]
	}
	mu.RUnlock()
	if tmpl == nil {
		return ""
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, payload); err != nil {
		return ""
	}
	return b.String()
}

// FormatResultHint renders the result hint for the given tool and result. Returns
// an empty string when no template is registered or rendering fails.
func FormatResultHint(id tools.Ident, result any) string {
	mu.RLock()
	tmpl := resultHints[id]
	if tmpl == nil {
		tmpl = resultHints[tools.Ident(id.Tool())]
	}
	mu.RUnlock()
	if tmpl == nil {
		return ""
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, result); err != nil {
		return ""
	}
	return b.String()
}

// CompileHintTemplates compiles text templates with a conservative default setup.
// Missing keys are ignored so authors can write templates that adapt to
// optional fields using if/with blocks without runtime errors.
func CompileHintTemplates(raw map[tools.Ident]string, extra template.FuncMap) (map[tools.Ident]*template.Template, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	funcs := template.FuncMap{
		"join": strings.Join,
		"count": func(v any) int {
			if xs, ok := v.([]any); ok {
				return len(xs)
			}
			return 0
		},
		"truncate": func(s string, n int) string {
			if n <= 0 {
				return ""
			}
			if len(s) <= n {
				return s
			}
			return s[:n]
		},
	}
	for k, v := range extra {
		funcs[k] = v
	}
	out := make(map[tools.Ident]*template.Template, len(raw))
	for id, src := range raw {
		if src == "" {
			continue
		}
		tmpl, err := template.New(string(id)).Funcs(funcs).Parse(src)
		if err != nil {
			return nil, fmt.Errorf("compile hint for %s: %w", id, err)
		}
		out[id] = tmpl
	}
	return out, nil
}
