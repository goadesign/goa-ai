// Package boundary preserves values exactly as workflow engines record them.
// Engine adapters use it to enforce the shared payload limit and to give
// in-process activities the same copied inputs and outputs as durable engines.
package boundary

import (
	"fmt"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"

	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	// Encoded contains one value after workflow-boundary encoding. Decode may be
	// called repeatedly to give each activity attempt a fresh value.
	Encoded[T any] struct {
		dataConverter converter.DataConverter
		payloads      *commonpb.Payloads
	}
)

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
// memory with the activity handler that produced it.
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
	total := 0
	for _, payload := range payloads.GetPayloads() {
		if payload == nil {
			continue
		}
		if err := addBytes(&total, len(payload.Data)); err != nil {
			return err
		}
		for key, value := range payload.Metadata {
			if err := addBytes(&total, len(key)); err != nil {
				return err
			}
			if err := addBytes(&total, len(value)); err != nil {
				return err
			}
		}
	}
	return nil
}

// addBytes rejects the next payload segment before integer addition can exceed
// the shared limit.
func addBytes(total *int, size int) error {
	if size > engine.MaxPayloadBytes-*total {
		return fmt.Errorf(
			"workflow payloads exceed maximum aggregate size %d bytes",
			engine.MaxPayloadBytes,
		)
	}
	*total += size
	return nil
}
