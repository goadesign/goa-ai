package tmplcheck

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"
	"text/template"
	"text/template/parse"
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

	// review09Collection models a custom owner returning a typed element for indexing and ranges.
	review09Collection struct {
		id string
	}
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

// ID supplies authored byte strings without restrictions.
func (r review09Collection) ID() string { return r.id }

// Member rejects fields because the represented value is a collection.
func (r review09Collection) Member(name string) (Type, error) {
	return nil, fmt.Errorf("collection has no field %s", name)
}

// Index returns the integer element contract.
func (r review09Collection) Index() (Type, error) {
	return ReflectType(reflect.TypeFor[int]()), nil
}

// Range returns the integer index and element contracts.
func (r review09Collection) Range(bool) (Type, Type, error) {
	return ReflectType(reflect.TypeFor[int]()), ReflectType(reflect.TypeFor[int]()), nil
}

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

// TestReview09StateEquality compares the new key against the former exact equality.
func TestReview09StateEquality(t *testing.T) {
	t.Parallel()

	ids := []string{"", "<dynamic>", "ut:X;$b=ut:Y", "ut:X", "ut:Y;$b=ut:Z", "ut:Z", ";", "=", ":", "0:", "1:x", "日本語;=$", "\x00", "\u200b", " ", "truth=true", "x:truth=false"}
	envs := make([]*varEnv, 0, 1+len(ids)*6+2*len(ids)+12)
	envs = append(envs, &varEnv{names: map[string]int{}, types: map[int]Type{}})
	for _, name := range []string{"$", "$a", "$b", "$é", "$变量", "$_0"} {
		for _, id := range ids {
			envs = append(envs, &varEnv{names: map[string]int{name: 31}, types: map[int]Type{31: &authoredType{id: id}}})
		}
	}
	for i, id := range ids {
		a := &varEnv{names: map[string]int{"$a": 1, "$b": 2}, types: map[int]Type{1: &authoredType{id: id}, 2: &authoredType{id: ids[(i+1)%len(ids)]}}}
		b := &varEnv{names: map[string]int{}, types: map[int]Type{78: ReflectType(reflect.TypeFor[int]())}}
		b.names["$b"] = 92
		b.names["$a"] = 91
		b.types[91] = a.types[1]
		b.types[92] = a.types[2]
		envs = append(envs, a, b)
	}
	for _, typ := range []Type{nil, nilDataType{}, ReflectType(reflect.TypeFor[int]()), newReflectType(reflect.TypeFor[int](), true), ReflectType(reflect.TypeFor[*int]()), ReflectType(reflect.TypeFor[[]int]()), withKnownTruth(nil, true), withKnownTruth(nil, false), withKnownTruth(ReflectType(reflect.TypeFor[int]()), true), withKnownTruth(ReflectType(reflect.TypeFor[int]()), false), withKnownTruth(&authoredType{id: "ut:x"}, true), withKnownTruth(&authoredType{id: "ut:x"}, false)} {
		envs = append(envs, &varEnv{names: map[string]int{"$a": 1}, types: map[int]Type{1: typ}})
	}
	keys := make([]string, len(envs))
	for i, env := range envs {
		keys[i] = envStateKey(env)
	}
	for i, a := range envs {
		for j, b := range envs {
			if (keys[i] == keys[j]) != envsEqual(a, b) {
				t.Fatalf("key equality differs at %d,%d: %q %q", i, j, keys[i], keys[j])
			}
		}
	}
	t.Logf("%d nonnil environments, %d ordered pairs match envsEqual; binding IDs/unreachable shadow state/order ignored as before", len(envs), len(envs)*len(envs))
}

// TestReview09StateIterationControl preserves a valid field after one iteration with authored IDs.
func TestReview09StateIterationControl(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"A1", "ut:X;$b=ut:Y", "ut:日本語;=$b=0:"} {
		t.Run(id, func(t *testing.T) {
			last := &authoredType{id: id, members: map[string]Type{"Label": ReflectType(reflect.TypeFor[string]())}}
			first := &authoredType{id: "first", members: map[string]Type{"Next": last}}
			root := &authoredType{id: "root", members: map[string]Type{"A": first}}
			src := `{{$a := .A}}{{range 1}}{{$a = $a.Next}}{{end}}{{$a.Label}}`
			parsed := template.Must(template.New("valid").Parse(src))
			if err := Check(parsed, root, nil); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := parsed.Execute(&out, struct{ A collisionA0 }{collisionA0{Next: collisionA1{Label: "first"}}}); err != nil {
				t.Fatal(err)
			}
			if out.String() != "first" {
				t.Fatal(out.String())
			}
		})
	}
}

// TestReview09CustomSlice checks direct/piped argument counting without reflection adapters.
func TestReview09CustomSlice(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, src                    string
		inputTruth, known, wantTruth bool
	}{
		{"direct_no_index", `{{slice .V}}`, true, true, true},
		{"piped_collection", `{{.V | slice}}`, true, true, true},
		{"false_no_index", `{{slice .V}}`, false, true, false},
		{"direct_low", `{{slice .V 0}}`, true, false, false},
		{"direct_low_high", `{{slice .V 0 0}}`, true, false, false},
		{"direct_low_high_max", `{{slice .V 0 0 1}}`, true, false, false},
		{"pipe_low", `{{0 | slice .V}}`, true, false, false},
		{"pipe_high", `{{0 | slice .V 0}}`, true, false, false},
		{"pipe_max", `{{1 | slice .V 0 0}}`, true, false, false},
		{"false_can_expand", `{{slice .V 0 1}}`, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := &authoredType{id: "root", members: map[string]Type{"V": withKnownTruth(review09Collection{"ut:bag;=$é"}, tc.inputTruth)}}
			parsed := template.Must(template.New("custom").Parse(tc.src))
			c := &checker{tmpl: parsed}
			res, err := c.evalPipe(parsed.Root.Nodes[0].(*parse.ActionNode).Pipe, root, c.newVarEnv(root))
			if err != nil {
				t.Fatal(err)
			}
			known, truth := staticTruth(nil, res)
			if known != tc.known || (known && truth != tc.wantTruth) {
				t.Errorf("known/truth=(%t,%t) want (%t,%t)", known, truth, tc.known, tc.wantTruth)
			}
			if typeID(withoutKnownTruth(res)) != "ut:bag;=$é" {
				t.Error("custom type identity lost")
			}
			value := []int{1}
			if !tc.inputTruth {
				value = make([]int, 0, 1)
			}
			var out bytes.Buffer
			oracle := parsed.Execute(&out, struct{ V []int }{value})
			t.Logf("known=%t truth=%t Go=%v output=%q", known, truth, oracle, out.String())
			if oracle != nil {
				t.Fatal(oracle)
			}
		})
	}
}
