package tmplcheck

import (
	"reflect"
	"testing"
	"text/template"
)

type recursiveNode struct {
	Name string
	Next *recursiveNode
}

type (
	typeA struct {
		OnlyOnA string
	}
	typeB struct {
		Other string
	}
	twoTypesRoot struct {
		A typeA
		B typeB
	}
)

type collectionsRoot struct {
	Names  []string
	Labels map[string]string
	Meta   map[string]any
}

func TestCheckRecursiveType(t *testing.T) {
	t.Parallel()

	rootType := ReflectType(reflect.TypeFor[*recursiveNode]())

	// Valid recursive template
	tmpl := template.Must(template.New("root").Parse(`{{ define "visit" }}{{ .Name }}{{ if .Next }}{{ template "visit" .Next }}{{ end }}{{ end }}{{ template "visit" . }}`))
	if err := Check(tmpl, rootType, nil); err != nil {
		t.Fatalf("expected valid recursive template to pass, got: %v", err)
	}

	// Invalid recursive template with undeclared field
	badTmpl := template.Must(template.New("root").Parse(`{{ define "visit" }}{{ .Next.Missing }}{{ end }}{{ template "visit" . }}`))
	if err := Check(badTmpl, rootType, nil); err == nil {
		t.Fatal("expected error for .Next.Missing, got nil")
	}
}

func TestCheckClosedEmptyStruct(t *testing.T) {
	t.Parallel()

	rootType := ReflectType(reflect.TypeFor[struct{}]())

	tmpl := template.Must(template.New("root").Parse(`Hello static text`))
	if err := Check(tmpl, rootType, nil); err != nil {
		t.Fatalf("expected static text to pass, got: %v", err)
	}

	badTmpl := template.Must(template.New("root").Parse(`Hello {{ .Missing }}`))
	if err := Check(badTmpl, rootType, nil); err == nil {
		t.Fatal("expected error for .Missing on empty struct, got nil")
	}
}

func TestCheckScalarCollections(t *testing.T) {
	t.Parallel()

	rootType := ReflectType(reflect.TypeFor[collectionsRoot]())

	validCases := []string{
		`{{ index .Names 0 }}`,
		`{{ .Labels.foo }}`,
		`{{ .Meta.foo.bar }}`,
	}
	for _, tc := range validCases {
		tmpl := template.Must(template.New("root").Parse(tc))
		if err := Check(tmpl, rootType, nil); err != nil {
			t.Fatalf("expected %q to pass, got: %v", tc, err)
		}
	}

	invalidCases := []string{
		`{{ (index .Names 0).Missing }}`,
		`{{ .Labels.foo.Missing }}`,
	}
	for _, tc := range invalidCases {
		tmpl := template.Must(template.New("root").Parse(tc))
		if err := Check(tmpl, rootType, nil); err == nil {
			t.Fatalf("expected %q to fail, got nil", tc)
		}
	}
}

func TestCheckNamedTemplateMultipleTypes(t *testing.T) {
	t.Parallel()

	rootType := ReflectType(reflect.TypeFor[twoTypesRoot]())

	// Template "sub" accesses .OnlyOnA. It is invoked with .A (valid) and .B (invalid).
	tmpl := template.Must(template.New("root").Parse(`{{ define "sub" }}{{ .OnlyOnA }}{{ end }}{{ template "sub" .A }}{{ template "sub" .B }}`))
	if err := Check(tmpl, rootType, nil); err == nil {
		t.Fatal("expected error when named template is invoked with typeB missing OnlyOnA, got nil")
	}
}

type methodTestStruct struct{}

func (methodTestStruct) ValidMethod() (string, error) {
	return "ok", nil
}

func (methodTestStruct) InvalidMethod(a, b int) (string, int, error) {
	return "bad", 1, nil
}

func TestCheckMethodReturnSignatures(t *testing.T) {
	t.Parallel()

	rootType := ReflectType(reflect.TypeFor[methodTestStruct]())

	tmpl := template.Must(template.New("root").Parse(`{{ .ValidMethod }}`))
	if err := Check(tmpl, rootType, nil); err != nil {
		t.Fatalf("expected ValidMethod to pass, got: %v", err)
	}

	badTmpl := template.Must(template.New("root").Parse(`{{ .InvalidMethod }}`))
	if err := Check(badTmpl, rootType, nil); err == nil {
		t.Fatal("expected InvalidMethod with 3 returns to fail, got nil")
	}
}
