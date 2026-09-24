package storage

// Literal parts carry opaque bytes through bounded storage commands. Their
// enclosing record supplies order; the final part closes one canonical value.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

type (
	// LiteralPart carries consecutive bytes of one canonical message array.
	// Parts must be contiguous until Final; none is a standalone message.
	LiteralPart struct {
		// Data contains nonempty bytes, which may split JSON or UTF-8 text.
		Data []byte
		// Final closes this literal. False must remain present on the wire.
		Final bool
	}
)

// UnmarshalJSON rejects missing fields before a decoded part reaches a store.
// Data is binary: UTF-8 validation here applies to the JSON envelope, not Data.
func (p *LiteralPart) UnmarshalJSON(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("literal part envelope contains invalid UTF-8")
	}
	var wire struct {
		Data  []byte
		Final *bool
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return fmt.Errorf("decode literal part: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("literal part contains trailing data")
	}
	if wire.Final == nil {
		return errors.New("literal part requires final")
	}
	value := LiteralPart{Data: wire.Data, Final: *wire.Final}
	if err := value.validate(); err != nil {
		return err
	}
	*p = value
	return nil
}

func (p LiteralPart) validate() error {
	if len(p.Data) == 0 {
		return errors.New("literal part requires nonempty data")
	}
	return nil
}
