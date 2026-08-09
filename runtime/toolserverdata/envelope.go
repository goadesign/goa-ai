// Package toolserverdata owns the common server-data envelope. Generated
// tool-specific canonicalizers own the closed set of kinds and payload codecs.
package toolserverdata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

type envelopeItem struct {
	Kind     string          `json:"kind"`
	Audience string          `json:"audience"`
	Data     json.RawMessage `json:"data"`
}

// Canonicalize strictly decodes the common envelope and delegates each
// kind-specific payload to the generated canonicalizer for one tool.
func Canonicalize(
	data rawjson.Message,
	canonicalizeItem func(kind, audience string, data rawjson.Message) (string, rawjson.Message, error),
) (rawjson.Message, error) {
	items, err := decodeEnvelope(data)
	if err != nil {
		return nil, err
	}
	canonical := make([]*envelopeItem, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if _, ok := seen[item.Kind]; ok {
			return nil, fmt.Errorf("server data kind %q appears more than once", item.Kind)
		}
		seen[item.Kind] = struct{}{}
		audience, payload, err := canonicalizeItem(item.Kind, item.Audience, rawjson.Message(item.Data))
		if err != nil {
			return nil, err
		}
		canonical = append(canonical, &envelopeItem{
			Kind:     item.Kind,
			Audience: audience,
			Data:     json.RawMessage(payload),
		})
	}
	return encodeEnvelope(canonical)
}

// decodeEnvelope strictly decodes common item fields while preserving each
// kind-specific payload as raw JSON for generated validation.
func decodeEnvelope(data rawjson.Message) ([]*envelopeItem, error) {
	if len(data) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var items []*envelopeItem
	if err := decoder.Decode(&items); err != nil {
		return nil, fmt.Errorf("decode server data envelope: %w", err)
	}
	if items == nil {
		return nil, fmt.Errorf("decode server data envelope: expected array")
	}
	if err := requireEOF(decoder); err != nil {
		return nil, err
	}
	for i, item := range items {
		if item == nil {
			return nil, fmt.Errorf("server data item %d is null", i)
		}
	}
	return items, nil
}

// encodeEnvelope encodes validated items into the canonical server-data
// envelope stored on planner results.
func encodeEnvelope(items []*envelopeItem) (rawjson.Message, error) {
	if len(items) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("encode server data envelope: %w", err)
	}
	return rawjson.Message(data), nil
}

// requireEOF rejects trailing JSON after the server-data array.
func requireEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode server data envelope: trailing JSON value")
		}
		return fmt.Errorf("decode server data envelope: %w", err)
	}
	return nil
}
