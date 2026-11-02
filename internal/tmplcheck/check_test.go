package tmplcheck

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"
	"text/template"
)

type (
	// authoredType implements Type with customizable canonical ID and field members.
	authoredType struct {
		id      string
		members map[string]Type
	}

	collisionA2 struct{ Missing string }
	collisionA1 struct {
		Next  collisionA2
		Label string
	}
	collisionA0 struct{ Next collisionA1 }
	collisionB2 struct{}
	collisionB1 struct{ Next collisionB2 }
	collisionB0 struct{ Next collisionB1 }
)

// ID returns the canonical type ID of the authored type.
func (a *authoredType) ID() string { return a.id }

// Member resolves member access against the authored type's members map.
func (a *authoredType) Member(n string) (Type, error) {
	v, ok := a.members[n]
	if !ok {
		return nil, fmt.Errorf("%s has no field %s", a.id, n)
	}
	return v, nil
}

// Index returns an error as authoredType is not an indexable collection.
func (a *authoredType) Index() (Type, error) { return nil, fmt.Errorf("not indexed") }

// Range returns an error as authoredType is not an iterable collection.
func (a *authoredType) Range(bool) (Type, Type, error) { return nil, nil, fmt.Errorf("not ranged") }

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

// TestReview8722ExternalStateIdentity compares a valid custom type owner with Go execution,
// verifying that delimiter-containing type IDs do not collide in state cycle keys.
func TestReview8722ExternalStateIdentity(t *testing.T) {
	t.Parallel()

	for _, delimited := range []bool{false, true} {
		t.Run(fmt.Sprint(delimited), func(t *testing.T) {
			ids := []string{"A0", "B0", "A1", "B1", "A2", "B2"}
			if delimited {
				ids = []string{"ut:X;$b=ut:Y", "ut:Z", "ut:X", "ut:Y;$b=ut:Z", "ut:A2", "ut:B2"}
			}
			all := make([]*authoredType, 6)
			for i, id := range ids {
				all[i] = &authoredType{id: id, members: map[string]Type{}}
			}
			all[0].members["Next"] = all[2]
			all[1].members["Next"] = all[3]
			all[2].members["Next"] = all[4]
			all[3].members["Next"] = all[5]
			all[2].members["Label"] = nil
			root := &authoredType{id: "root", members: map[string]Type{"A": all[0], "B": all[1]}}
			src := `{{$a := .A}}{{$b := .B}}{{range 2}}{{$a = $a.Next}}{{$b = $b.Next}}{{end}}{{$a.Label}}`
			parsed := template.Must(template.New("root").Parse(src))
			checked := Check(parsed, root, nil)
			var out bytes.Buffer
			oracle := parsed.Execute(&out, struct {
				A collisionA0
				B collisionB0
			}{A: collisionA0{Next: collisionA1{Label: "first"}}})
			t.Logf("ids=%q checked=%v oracle=%v output=%q", ids, checked, oracle, out.String())
			if oracle == nil {
				t.Fatal("fixture must reject missing field after two iterations")
			}
			if checked == nil {
				t.Error("checker skipped second iteration and accepted absent Label")
			}
		})
	}
}
