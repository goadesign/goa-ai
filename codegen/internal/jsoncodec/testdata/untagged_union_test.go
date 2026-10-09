// These checks use the generated public codecs. Raw JSON retains exact typed
// values; invalid input returns an error before a service value is constructed.
package types_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gentypes "codec.local/gen/types"
)

func TestUntaggedManifestCodec(t *testing.T) {
	for _, value := range []*gentypes.Entry{
		{Resources: gentypes.NewResourcesDynamic("dynamic")},
		{Resources: gentypes.NewResourcesManifest([]*gentypes.File{})},
		{Resources: gentypes.NewResourcesManifest([]*gentypes.File{{Address: "file://record"}})},
	} {
		before := *value
		encoded, err := gentypes.EncodeEntry(value)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), `"type":`)
		decoded, err := gentypes.DecodeEntry(encoded)
		require.NoError(t, err)
		assert.Equal(t, value, decoded)
		assert.Equal(t, before, *value)
	}
	for _, document := range []string{
		`{}`, `{"resources":null}`, `{"resources":true}`, `{"resources":1}`,
		`{"resources":"other"}`, `{"resources":[null]}`, `{"resources":[{}]}`,
		`{"resources":[{"uri":""}]}`, `{"resources":[{"uri":null}]}`,
		`{"resources":[{"uri":"file://record","other":true}]}`,
		`{"resources":[{"uri":"file://record","uri":"file://other"}]}`,
		`{"resources":"dynamic","resources":[]}`,
		`{"resources":{"type":"dynamic","value":"dynamic"}}`,
	} {
		value, err := gentypes.DecodeEntry([]byte(document))
		assert.Error(t, err, document)
		assert.Nil(t, value)
	}
	for _, value := range []*gentypes.Entry{
		{}, {Resources: gentypes.NewResourcesDynamic("other")},
		{Resources: gentypes.NewResourcesManifest([]*gentypes.File{nil})},
		{Resources: gentypes.NewResourcesManifest([]*gentypes.File{{}})},
	} {
		before := *value
		data, err := gentypes.EncodeEntry(value)
		assert.Error(t, err)
		assert.Nil(t, data)
		assert.True(t, reflect.DeepEqual(before, *value))
	}
}

func TestUntaggedExactValueCodec(t *testing.T) {
	for _, value := range []*gentypes.Selection{
		{Value: gentypes.NewChoiceText("")},
		{Value: gentypes.NewChoiceNumber(9007199254740993)},
		{Value: gentypes.NewChoiceEnabled(false)},
		{Value: gentypes.NewChoiceRecord(&gentypes.File{Address: "file://record"})},
		{Value: gentypes.NewChoiceFiles([]*gentypes.File{})},
	} {
		data, err := gentypes.EncodeSelection(value)
		require.NoError(t, err)
		decoded, err := gentypes.DecodeSelection(data)
		require.NoError(t, err)
		assert.Equal(t, value, decoded)
	}
	for _, document := range []string{
		`{"content":"AQI="}`, `{"content":{}}`, `{"content":{"value":9007199254740993}}`,
	} {
		value, err := gentypes.DecodeBinary([]byte(document))
		require.NoError(t, err)
		data, err := gentypes.EncodeBinary(value)
		require.NoError(t, err)
		assert.JSONEq(t, document, string(data))
	}
	for _, document := range []string{
		`{"value":1.5}`, `{"value":9223372036854775808}`,
	} {
		_, err := gentypes.DecodeSelection([]byte(document))
		assert.Error(t, err, document)
	}
	_, err := gentypes.DecodeBinary([]byte(`{"content":"not base64"}`))
	assert.Error(t, err)
}
