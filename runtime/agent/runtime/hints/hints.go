// Package hints provides lightweight, template-based rendering helpers for
// tool call/result hints. It exposes a global registry usable by sinks without
// requiring a runtime instance.
package hints

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"text/template"
	"time"

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

// RenderCallHint renders the registered call hint template for id.
//
// The boolean return reports whether a template was registered. A registered
// template must render a non-empty string; render errors and empty output are
// returned as contract violations.
func RenderCallHint(id tools.Ident, payload any) (string, bool, error) {
	mu.RLock()
	tmpl := callHints[id]
	mu.RUnlock()
	return renderHint("call", id, tmpl, payload)
}

// RenderResultHint renders the registered result hint template for id.
//
// The boolean return reports whether a template was registered. A registered
// template must render a non-empty string; render errors and empty output are
// returned as contract violations.
func RenderResultHint(id tools.Ident, result any) (string, bool, error) {
	mu.RLock()
	tmpl := resultHints[id]
	mu.RUnlock()
	return renderHint("result", id, tmpl, result)
}

// CompileHintTemplates compiles text templates with a conservative default setup.
// Missing keys are errors; authors must use if/with blocks for optional fields.
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
		// humanTime renders a timestamp for user-facing hints at minute precision.
		// It accepts RFC3339 timestamp strings and time.Time values.
		//
		// When parsing fails, it returns fmt.Sprint(v) so hint rendering can
		// continue while still surfacing the original value.
		"humanTime": func(v any) string {
			ts, ok := parseTimestamp(v)
			if !ok {
				return fmt.Sprint(v)
			}
			return ts.Format("Jan 2, 3:04 PM")
		},
		// since returns the integer number of seconds between two timestamps:
		// to - from.
		//
		// It accepts RFC3339 timestamp strings (including timezone offsets) and
		// time.Time values. When parsing fails, it returns 0 so hint rendering
		// can continue.
		"since": func(from, to any) int64 {
			a, ok := parseTimestamp(from)
			if !ok {
				return 0
			}
			b, ok := parseTimestamp(to)
			if !ok {
				return 0
			}
			return int64(b.Sub(a).Seconds())
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
		// number converts scalar numeric values, including generated optional
		// numeric pointers, into float64 for printf formatting in hints.
		"number": number,
	}
	for k, v := range extra {
		funcs[k] = v
	}
	out := make(map[tools.Ident]*template.Template, len(raw))
	for id, src := range raw {
		if src == "" {
			continue
		}
		tmpl, err := template.New(string(id)).Option("missingkey=error").Funcs(funcs).Parse(src)
		if err != nil {
			return nil, fmt.Errorf("compile hint for %s: %w", id, err)
		}
		out[id] = tmpl
	}
	return out, nil
}

// renderHint executes a registered hint template and enforces the non-empty
// preview invariant shared by call and result hints.
func renderHint(kind string, id tools.Ident, tmpl *template.Template, data any) (string, bool, error) {
	if tmpl == nil {
		return "", false, nil
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		return "", true, fmt.Errorf("render %s hint for %s: %w", kind, id, err)
	}
	out := b.String()
	if strings.TrimSpace(out) == "" {
		return "", true, fmt.Errorf("render %s hint for %s: empty output", kind, id)
	}
	return out, true, nil
}

// number dereferences typed numeric values for hint templates. Template authors
// use it with printf so optional generated fields never render as Go pointers.
func number(v any) (float64, error) {
	if v == nil {
		return 0, fmt.Errorf("number: nil value")
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return 0, fmt.Errorf("number: nil pointer")
		}
		rv = rv.Elem()
	}
	kind := rv.Kind()
	switch {
	case kind >= reflect.Int && kind <= reflect.Int64:
		return float64(rv.Int()), nil
	case kind >= reflect.Uint && kind <= reflect.Uintptr:
		return float64(rv.Uint()), nil
	case kind == reflect.Float32 || kind == reflect.Float64:
		return rv.Float(), nil
	default:
		return 0, fmt.Errorf("number: unsupported %T", v)
	}
}

func parseTimestamp(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case *time.Time:
		if t == nil {
			return time.Time{}, false
		}
		return *t, true
	case string:
		if t == "" {
			return time.Time{}, false
		}
		ts, err := time.Parse(time.RFC3339, t)
		if err != nil {
			return time.Time{}, false
		}
		return ts, true
	default:
		s := fmt.Sprint(v)
		if s == "" {
			return time.Time{}, false
		}
		ts, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}, false
		}
		return ts, true
	}
}
