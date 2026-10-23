// These tests check that reused Go type facts never reuse a prior JSON value's
// acceptance, selected union branch, or original consumer contract bytes.
package admission

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

type (
	savedFactsFirst struct {
		Ignored string `json:"-"`
		// The reader must ignore this unexported field when checking JSON names.
		hidden   bool   //nolint:unused // Reflection checks that JSON cannot select this field.
		First    string `json:"label"`
		Default  string
		EmptyTag string `json:",omitempty"` //nolint:tagliatelle // An empty JSON name must use the exported Go field name.
	}
	savedFactsSecond struct {
		Selected bool `json:"label"`
	}
)

func TestSavedTypeFactsPreserveExactFieldRules(t *testing.T) {
	matchingTags := reflect.StructOf([]reflect.StructField{
		{Name: "First", Type: reflect.TypeFor[string](), Tag: `json:"label"`},
		{Name: "Second", Type: reflect.TypeFor[int](), Tag: `json:"label"`},
	})
	for _, test := range []struct {
		name    string
		target  reflect.Type
		field   string
		want    reflect.Type
		unknown bool
	}{
		{name: "first matching tag", target: matchingTags, field: "label", want: reflect.TypeFor[string]()},
		{name: "explicit tag", target: reflect.TypeFor[savedFactsFirst](), field: "label", want: reflect.TypeFor[string]()},
		{name: "different Go type", target: reflect.TypeFor[savedFactsSecond](), field: "label", want: reflect.TypeFor[bool]()},
		{name: "implicit exported name", target: reflect.TypeFor[savedFactsFirst](), field: "Default", want: reflect.TypeFor[string]()},
		{name: "empty tag", target: reflect.TypeFor[savedFactsFirst](), field: "EmptyTag", want: reflect.TypeFor[string]()},
		{name: "wrong case", target: reflect.TypeFor[savedFactsFirst](), field: "default", unknown: true},
		{name: "ignored field", target: reflect.TypeFor[savedFactsFirst](), field: "Ignored", unknown: true},
		{name: "unexported field", target: reflect.TypeFor[savedFactsFirst](), field: "hidden", unknown: true},
		{name: "map keys remain data", target: reflect.TypeFor[map[string]int](), field: "any customer key", want: reflect.TypeFor[int]()},
		{name: "scalar unchanged", target: reflect.TypeFor[string](), field: "anything"},
		{name: "pointer unchanged", target: reflect.TypeFor[*savedFactsFirst](), field: "label"},
		{name: "unknown target unchanged", field: "anything"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for range 2 {
				got, err := savedMemberType(test.target, test.field)
				if test.unknown {
					assert.EqualError(t, err, fmt.Sprintf("unknown field %q in %s; saved member names must match generated JSON names exactly", test.field, test.target.Name()))
				} else {
					require.NoError(t, err)
					assert.Equal(t, test.want, got)
				}
			}
		})
	}
}

func TestSavedDeclarationWarmedTypesStillRejectChangedJSON(t *testing.T) {
	valid := []byte(`{"Name":"records","Tools":[{"Name":"records.read","ConsumerContract":{"Kind":"service","Payload":{"Fields":[{"Path":[{"Segment":{"type":"element","value":{}}}]}]}}}]}`)
	for _, test := range []struct{ name, raw, want string }{
		{name: "unknown name", raw: `{"Name":"records","Unknown":true}`, want: `unknown field "Unknown"`},
		{name: "wrong case", raw: `{"name":"records"}`, want: `unknown field "name"`},
		{name: "escaped duplicate", raw: `{"Name":"records","Na\u006de":"other"}`, want: `duplicate JSON member "Name"`},
		{name: "branch unknown member", raw: strings.Replace(string(valid), `"value":{}`, `"value":{"unknown":true}`, 1), want: `unknown field "unknown"`},
		{name: "union wrong case", raw: strings.Replace(string(valid), `"type":"element"`, `"Type":"element"`, 1), want: `unknown field "Type"`},
		{name: "union duplicate", raw: strings.Replace(string(valid), `"value":{}`, `"value":{},"value":{}`, 1), want: `duplicate JSON member "value"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := SavedToolsetFingerprint(valid)
			require.NoError(t, err)
			_, _, err = SavedToolsetFingerprint([]byte(test.raw))
			assert.ErrorContains(t, err, test.want)
		})
	}
}

func TestSavedDeclarationWarmedTypesKeepBranchAndRawBytes(t *testing.T) {
	for _, segment := range []string{`{"type":"field","value":"record"}`, `{"type":"element","value":{}}`, `{"type":"field","value":"changed"}`} {
		contract := `{ "Kind" : "service", "Title":"Read \u0072ecords", "Payload":{"Fields":[{"Path":[{"Segment":` + segment + `}]}]} }`
		raw := []byte(`{"Name":"records","Tools":[{"Name":"records.read","ConsumerContract":` + contract + `}]}`)
		toolset, contracts, err := parseSavedDeclaration(raw)
		require.NoError(t, err)
		require.Len(t, contracts, 1)
		assert.Equal(t, contract, string(contracts[0]))
		start := bytes.Index(raw, contracts[0])
		require.NotEqual(t, -1, start)
		assert.Same(t, &raw[start], &contracts[0][0])
		selected := toolset.Tools[0].ConsumerContract.Payload.Fields[0].Path[0].Segment
		if strings.Contains(segment, `"type":"element"`) {
			assert.Equal(t, genregistry.ToolFieldSegmentKindElement, selected.Kind())
		} else {
			field, ok := selected.AsField()
			assert.True(t, ok)
			if strings.Contains(segment, `"changed"`) {
				assert.Equal(t, genregistry.ToolFieldSegmentBranchField("changed"), field)
			} else {
				assert.Equal(t, genregistry.ToolFieldSegmentBranchField("record"), field)
			}
		}
		_, fingerprint, err := SavedToolsetFingerprint(raw)
		require.NoError(t, err)
		assert.Equal(t, SchemaFingerprint(Schema{Name: "records", Tools: []ToolSchema{{Name: "records.read", ConsumerContract: []byte(contract)}}}), fingerprint)
	}
}

func TestSavedDeclarationWarmedTypesKeepHistoricalIntegrity(t *testing.T) {
	contract := `{ "Kind":"service", "Bounds":{"Paging":{"ReplayPayload":true}} }`
	raw := []byte(`{"Name":"records","Tools":[{"Name":"records.read","ConsumerContract":` + contract + `}]}`)
	info := &genregistry.ToolsetInfo{Name: "records", ToolCount: 1}
	want := SchemaFingerprint(Schema{Name: "records", Tools: []ToolSchema{{Name: "records.read", ConsumerContract: []byte(contract)}}})
	for range 2 {
		_, _, err := SavedToolsetFingerprint(raw)
		require.ErrorContains(t, err, "ReplayPayload")
		got, err := ValidateSavedToolsetIntegrity(raw, info, want)
		require.NoError(t, err)
		assert.Equal(t, want, SchemaFingerprint(got))
		duplicate := strings.Replace(string(raw), `"ReplayPayload":true`, `"ReplayPayload":true,"ReplayPayload":true`, 1)
		_, err = ValidateSavedToolsetIntegrity([]byte(duplicate), info, want)
		assert.ErrorContains(t, err, "duplicate")
	}
}

func TestSavedTypeFactsConcurrentFirstUseAndSelectedValues(t *testing.T) {
	type firstBranch struct {
		First string `json:"selected"`
	}
	type secondBranch struct {
		Second bool `json:"selected"`
	}
	branches := []reflect.Type{reflect.TypeFor[firstBranch](), reflect.TypeFor[secondBranch]()}
	start := make(chan struct{})
	results := make(chan error, 16)
	var workers sync.WaitGroup
	for index := range 16 {
		workers.Go(func() {
			<-start
			branch := branches[index%len(branches)]
			member, err := savedMemberType(branch, "selected")
			if err != nil {
				results <- err
				return
			}
			if member != branch.Field(0).Type {
				results <- fmt.Errorf("selected member belongs to another Go type")
				return
			}
			envelope := savedUnionEnvelope(branch)
			if envelope.Field(1).Type != branch {
				results <- fmt.Errorf("union envelope belongs to another selected Go type")
				return
			}
			for _, segment := range []string{`{"type":"field","value":"record"}`, `{"type":"element","value":{}}`} {
				raw := []byte(`{"Name":"records","Tools":[{"Name":"records.read","ConsumerContract":{"Kind":"service","Payload":{"Fields":[{"Path":[{"Segment":` + segment + `}]}]}}}]}`)
				if _, _, err := SavedToolsetFingerprint(raw); err != nil {
					results <- err
					return
				}
				invalid := strings.Replace(string(raw), `"type":`, `"Type":`, 1)
				if _, _, err := SavedToolsetFingerprint([]byte(invalid)); err == nil {
					results <- fmt.Errorf("concurrent read accepted wrong union member case")
					return
				}
			}
			results <- nil
		})
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		assert.NoError(t, err)
	}
}
