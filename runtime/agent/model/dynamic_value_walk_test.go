package model

// dynamic_value_walk_test.go verifies that custom adapter metadata cannot make
// response copying or fingerprinting recurse or expand without a fixed limit.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type dynamicByteStruct struct {
	Value string `json:"value"`
}

const responseWithMetadataFixedBytes = len("assistant") + len("answer") + len("value") + len("end_turn")

func TestDynamicValueWalkEnforcesDepthBoundary(t *testing.T) {
	walk := &dynamicValueWalk{}
	_, _, err := walk.enter(reflect.ValueOf("value"), maxDynamicValueDepth)
	require.NoError(t, err)

	_, _, err = walk.enter(reflect.ValueOf("value"), maxDynamicValueDepth+1)
	require.ErrorContains(t, err, "exceeds maximum depth")
}

func TestDynamicValueWalkEnforcesVisitBoundary(t *testing.T) {
	walk := &dynamicValueWalk{}
	for range maxDynamicValueVisits {
		_, _, err := walk.enter(reflect.ValueOf("value"), 0)
		require.NoError(t, err)
	}

	_, _, err := walk.enter(reflect.ValueOf("value"), 0)
	require.ErrorContains(t, err, "exceeds maximum visited values")
}

func TestResponseMetadataDepthLimitMatchesCopyAndFingerprint(t *testing.T) {
	atLimit := responseWithNestedMetadata(maxDynamicValueDepth - 1)
	_, err := fingerprintResponse(atLimit)
	require.NoError(t, err)
	_, err = ownResponse(atLimit)
	require.NoError(t, err)

	aboveLimit := responseWithNestedMetadata(maxDynamicValueDepth)
	_, err = fingerprintResponse(aboveLimit)
	require.ErrorContains(t, err, "exceeds maximum depth")
	_, err = ownResponse(aboveLimit)
	require.ErrorContains(t, err, "exceeds maximum depth")
}

func TestResponseMetadataCyclesFailCopyAndFingerprint(t *testing.T) {
	cyclicMap := map[string]any{}
	cyclicMap["self"] = cyclicMap
	cyclicSlice := make([]any, 1)
	cyclicSlice[0] = cyclicSlice

	for name, metadata := range map[string]any{
		"map":   cyclicMap,
		"slice": cyclicSlice,
	} {
		t.Run(name, func(t *testing.T) {
			response := responseWithMetadata(metadata)

			_, fingerprintErr := fingerprintResponse(response)
			_, copyErr := ownResponse(response)

			require.ErrorContains(t, fingerprintErr, "reference cycle")
			require.ErrorContains(t, copyErr, "reference cycle")
		})
	}
}

func TestResponseMetadataByteLimitMatchesCopyAndFingerprint(t *testing.T) {
	atLimit := responseWithMetadata(strings.Repeat("x", maxDynamicValueBytes-responseWithMetadataFixedBytes))
	_, err := fingerprintResponse(atLimit)
	require.NoError(t, err)
	_, err = ownResponse(atLimit)
	require.NoError(t, err)

	aboveLimit := responseWithMetadata(strings.Repeat("x", maxDynamicValueBytes-responseWithMetadataFixedBytes+1))
	_, err = fingerprintResponse(aboveLimit)
	require.ErrorContains(t, err, "exceeds maximum byte size")
	_, err = ownResponse(aboveLimit)
	require.ErrorContains(t, err, "exceeds maximum byte size")
}

func TestResponseMetadataVisitLimitMatchesCopyAndFingerprint(t *testing.T) {
	values := make([]any, maxDynamicValueVisits)
	for index := range values {
		values[index] = index
	}
	response := responseWithMetadata(values)

	_, err := fingerprintResponse(response)
	require.ErrorContains(t, err, "exceeds maximum visited values")
	_, err = ownResponse(response)
	require.ErrorContains(t, err, "exceeds maximum visited values")
}

func TestResponseBudgetAggregatesAcrossMetadataFields(t *testing.T) {
	response := &Response{
		Content: []Message{{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: "answer"}},
			Meta: map[string]any{
				"first":  strings.Repeat("a", maxDynamicValueBytes/2),
				"second": strings.Repeat("b", maxDynamicValueBytes/2),
			},
		}},
		StopReason: "end_turn",
	}

	_, fingerprintErr := fingerprintResponse(response)
	_, copyErr := ownResponse(response)

	require.ErrorContains(t, fingerprintErr, "exceeds maximum byte size")
	require.ErrorContains(t, copyErr, "exceeds maximum byte size")
}

func TestCanonicalDynamicValuesRejectStructsAndInvalidUTF8(t *testing.T) {
	invalid := string([]byte{0xff})
	for name, metadata := range map[string]map[string]any{
		"struct":         {"value": struct{ Value string }{Value: "not canonical"}},
		"invalid string": {"value": invalid},
		"invalid key":    {invalid: "value"},
	} {
		t.Run(name, func(t *testing.T) {
			response := responseWithMetadata(metadata)
			err := ValidateResponse(response)
			require.Error(t, err)
			if name == "struct" {
				require.ErrorContains(t, err, "not JSON-compatible metadata")
			} else {
				require.ErrorContains(t, err, "not valid UTF-8")
			}
		})
	}
}

func TestCanonicalMapErrorsAreDeterministic(t *testing.T) {
	metadata := map[string]any{
		"z": make(chan int),
		"a": struct{ Value string }{Value: "first"},
	}
	var first string
	for range 20 {
		_, err := CloneMessages([]*Message{{Meta: metadata}})
		require.Error(t, err)
		if first == "" {
			first = err.Error()
		}
		require.Equal(t, first, err.Error())
	}
	require.Contains(t, first, "struct")
}

func TestResponseMetadataNilVisitLimitMatchesCopyAndFingerprint(t *testing.T) {
	atLimit := responseWithMetadata(make([]any, maxDynamicValueVisits-6))
	_, err := fingerprintResponse(atLimit)
	require.NoError(t, err)
	_, err = ownResponse(atLimit)
	require.NoError(t, err)

	aboveLimit := responseWithMetadata(make([]any, maxDynamicValueVisits-5))
	_, err = fingerprintResponse(aboveLimit)
	require.ErrorContains(t, err, "exceeds maximum visited values")
	_, err = ownResponse(aboveLimit)
	require.ErrorContains(t, err, "exceeds maximum visited values")
}

func TestResponseMetadataStructByteLimitMatchesCopyAndFingerprint(t *testing.T) {
	valueType := reflect.TypeFor[dynamicByteStruct]()
	field := valueType.Field(0)
	descriptorBytes := len(valueType.PkgPath()) + len(valueType.Name()) + len(field.Name) + len(field.Tag)
	atLimit := responseWithMetadata(dynamicByteStruct{
		Value: strings.Repeat("x", maxDynamicValueBytes-responseWithMetadataFixedBytes-descriptorBytes),
	})
	_, err := fingerprintResponse(atLimit)
	require.NoError(t, err)
	_, err = ownResponse(atLimit)
	require.NoError(t, err)

	aboveLimit := responseWithMetadata(dynamicByteStruct{
		Value: strings.Repeat("x", maxDynamicValueBytes-responseWithMetadataFixedBytes-descriptorBytes+1),
	})
	_, err = fingerprintResponse(aboveLimit)
	require.ErrorContains(t, err, "exceeds maximum byte size")
	_, err = ownResponse(aboveLimit)
	require.ErrorContains(t, err, "exceeds maximum byte size")
}

func TestFingerprintResponseOrdersStructFieldsByName(t *testing.T) {
	first := responseWithMetadata(struct {
		B string
		A int
	}{B: "two", A: 1})
	second := responseWithMetadata(struct {
		A int
		B string
	}{A: 1, B: "two"})

	firstFingerprint, err := fingerprintResponse(first)
	require.NoError(t, err)
	secondFingerprint, err := fingerprintResponse(second)
	require.NoError(t, err)

	assert.Equal(t, firstFingerprint, secondFingerprint)
}

func TestFingerprintResponseIncludesStructFieldTags(t *testing.T) {
	first := responseWithMetadata(struct {
		Value string `json:"first"`
	}{Value: "same"})
	second := responseWithMetadata(struct {
		Value string `json:"second"`
	}{Value: "same"})

	firstFingerprint, err := fingerprintResponse(first)
	require.NoError(t, err)
	secondFingerprint, err := fingerprintResponse(second)
	require.NoError(t, err)

	assert.NotEqual(t, firstFingerprint, secondFingerprint)
}

func responseWithNestedMetadata(depth int) *Response {
	var metadata any = "leaf"
	for range depth {
		metadata = map[string]any{"next": metadata}
	}
	return responseWithMetadata(metadata)
}

func responseWithMetadata(metadata any) *Response {
	return &Response{
		Content: []Message{{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: "answer"}},
			Meta:  map[string]any{"value": metadata},
		}},
		StopReason: "end_turn",
	}
}
