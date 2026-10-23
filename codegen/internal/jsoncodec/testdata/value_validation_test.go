// Generated original-value validators check fields before conversion and leave
// the caller's objects, maps, slices and selected union branches unchanged.
package types

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStandaloneFieldValidation(t *testing.T) {
	wrappedNegative, widePositive := int64(-4294967295), int64(4294967296)
	for _, test := range []struct {
		name         string
		change       func(*LookupResult)
		invalid      bool
		needsWideInt bool
	}{
		{name: "complete"},
		{name: "schema only", change: func(v *LookupResult) { v.Definition.Tools[0].Metadata = nil }},
		{needsWideInt: true, name: "wide positive length", change: func(v *LookupResult) { v.Definition.Tools[0].Metadata.Search.Length = int(widePositive) }},
		{needsWideInt: true, name: "wide positive term", change: func(v *LookupResult) { v.Definition.Tools[0].Metadata.Search.Terms["inspect"] = int(widePositive) }},
		{needsWideInt: true, name: "wide positive index", change: func(v *LookupResult) {
			v.Definition.Tools[0].Metadata.Fields[0].Branch = NewBranchUntagged(&Untagged{Index: int(widePositive), Kind: "object"})
		}},
		{name: "missing selection", invalid: true, change: func(v *LookupResult) { v.Selection = nil }},
		{name: "missing definition", invalid: true, change: func(v *LookupResult) { v.Definition = nil }},
		{name: "missing search", invalid: true, change: func(v *LookupResult) { v.Definition.Tools[0].Metadata.Search = nil }},
		{name: "missing schema", invalid: true, change: func(v *LookupResult) { v.Definition.Tools[0].Schema = nil }},
		{needsWideInt: true, name: "wrapped negative length", invalid: true, change: func(v *LookupResult) { v.Definition.Tools[0].Metadata.Search.Length = int(wrappedNegative) }},
		{needsWideInt: true, name: "wrapped negative term", invalid: true, change: func(v *LookupResult) { v.Definition.Tools[0].Metadata.Search.Terms["inspect"] = int(wrappedNegative) }},
		{needsWideInt: true, name: "wrapped negative index", invalid: true, change: func(v *LookupResult) {
			v.Definition.Tools[0].Metadata.Fields[0].Branch = NewBranchUntagged(&Untagged{Index: int(wrappedNegative), Kind: "object"})
		}},
		{name: "bad version", invalid: true, change: func(v *LookupResult) { bad := SemVer("invalid"); v.Definition.Version = &bad }},
		{name: "invalid UTF8", invalid: true, change: func(v *LookupResult) { v.Selection.Name = string([]byte{255}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.needsWideInt && strconv.IntSize < 64 {
				t.Skip("the original Go int cannot represent this value")
			}
			value, before := validLookup(), validLookup()
			if test.change != nil {
				test.change(value)
				test.change(before)
			}
			validationErr := ValidateLookupResultValue(value)
			_, encodeErr := EncodeLookupResult(value)
			if test.invalid {
				assert.Error(t, validationErr)
				assert.Error(t, encodeErr)
			} else {
				assert.NoError(t, validationErr)
				assert.NoError(t, encodeErr)
			}
			assert.Equal(t, before, value, "validation must leave original input unchanged")
		})
	}
	assert.Error(t, ValidateLookupResultValue(nil))
	_, err := EncodeLookupResult(nil)
	assert.Error(t, err)
}

// validLookup creates a complete typed value so each test changes one field.
func validLookup() *LookupResult {
	version := SemVer("1.2.3")
	metadata := &Metadata{
		Title:  "Inspect",
		Search: &Search{Length: 1, Terms: map[string]int{"inspect": 1}},
		Fields: []*Field{{Branch: NewBranchUntagged(&Untagged{Index: 0, Kind: "object"})}},
	}
	return &LookupResult{
		Selection: &Selection{Name: "records.inspect"},
		Definition: &Definition{
			Version: &version,
			Tools:   []*Tool{{Name: "records.inspect", Schema: []byte(`{"type":"object"}`), Metadata: metadata}},
		},
	}
}
