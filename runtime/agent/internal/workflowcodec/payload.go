// Package workflowcodec snapshots workflow values and enforces one aggregate
// payload limit before engines persist or submit them.
package workflowcodec

import (
	"fmt"
	"unicode/utf8"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"

	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	// Budget counts workflow text and encoded payload bytes without retaining
	// caller values. Callers add each value before copying or storing it.
	Budget struct {
		used         int
		sourceVisits int
	}

	// Encoded contains one value after workflow-boundary encoding. Decode may be
	// called repeatedly to give each activity attempt a fresh value.
	Encoded[T any] struct {
		dataConverter converter.DataConverter
		payloads      *commonpb.Payloads
	}
)

// AddText adds UTF-8 text to the workflow byte total.
func (b *Budget) AddText(values ...string) error {
	for _, value := range values {
		if !utf8.ValidString(value) {
			return fmt.Errorf("workflow codec: text contains invalid UTF-8")
		}
	}
	for _, value := range values {
		if err := b.addBytes(len(value)); err != nil {
			return err
		}
	}
	return nil
}

// AddSource adds the bytes visible in caller-owned values before an encoder
// can allocate a larger representation or replace invalid text.
func (b *Budget) AddSource(values ...any) error {
	preflight := &workflowJSONPreflight{visits: b.sourceVisits, budget: b}
	for _, value := range values {
		if err := preflight.add(value); err != nil {
			return err
		}
		b.sourceVisits = preflight.visits
	}
	return nil
}

// AddPayload adds one encoded payload's data and metadata bytes.
func (b *Budget) AddPayload(payload *commonpb.Payload) error {
	if payload == nil {
		return nil
	}
	for key := range payload.Metadata {
		if !utf8.ValidString(key) {
			return fmt.Errorf("workflow codec: payload metadata key contains invalid UTF-8")
		}
	}
	if err := b.addBytes(len(payload.Data)); err != nil {
		return err
	}
	for key, value := range payload.Metadata {
		if err := b.addBytes(len(key)); err != nil {
			return err
		}
		if err := b.addBytes(len(value)); err != nil {
			return err
		}
	}
	return nil
}

// AddPayloads adds every encoded payload in order.
func (b *Budget) AddPayloads(payloads *commonpb.Payloads) error {
	for _, payload := range payloads.GetPayloads() {
		if err := b.AddPayload(payload); err != nil {
			return err
		}
	}
	return nil
}

// Encode converts value with dataConverter and applies the shared aggregate
// payload limit to the exact encoded bytes and metadata.
func Encode[T any](dataConverter converter.DataConverter, value T) (*Encoded[T], error) {
	payloads, err := dataConverter.ToPayloads(value)
	if err != nil {
		return nil, err
	}
	if err := ValidatePayloads(payloads); err != nil {
		return nil, err
	}
	return &Encoded[T]{dataConverter: dataConverter, payloads: payloads}, nil
}

// Decode returns a new value reconstructed from the recorded payload.
func (e *Encoded[T]) Decode() (T, error) {
	var value T
	if err := e.dataConverter.FromPayloads(e.payloads, &value); err != nil {
		return value, err
	}
	return value, nil
}

// Copy records and reconstructs one value so the result cannot share mutable
// memory with the code that produced it.
func Copy[T any](dataConverter converter.DataConverter, value T) (T, error) {
	encoded, err := Encode(dataConverter, value)
	if err != nil {
		var zero T
		return zero, err
	}
	return encoded.Decode()
}

// ValidatePayloads applies one aggregate limit to payload data and metadata.
func ValidatePayloads(payloads *commonpb.Payloads) error {
	return new(Budget).AddPayloads(payloads)
}

// copyPayload gives the caller independent metadata and data bytes.
func copyPayload(payload *commonpb.Payload) *commonpb.Payload {
	if payload == nil {
		return nil
	}
	metadata := make(map[string][]byte, len(payload.Metadata))
	for key, value := range payload.Metadata {
		metadata[key] = append([]byte(nil), value...)
	}
	return &commonpb.Payload{Metadata: metadata, Data: append([]byte(nil), payload.Data...)}
}

// singlePayload wraps one encoded value for the shared aggregate validator.
func singlePayload(payload *commonpb.Payload) *commonpb.Payloads {
	return &commonpb.Payloads{Payloads: []*commonpb.Payload{payload}}
}

// addBytes rejects the next payload segment before integer addition can exceed
// the shared limit.
func (b *Budget) addBytes(size int) error {
	if size < 0 || size > engine.MaxPayloadBytes-b.used {
		return fmt.Errorf(
			"workflow codec: payloads exceed maximum aggregate size %d bytes",
			engine.MaxPayloadBytes,
		)
	}
	b.used += size
	return nil
}
