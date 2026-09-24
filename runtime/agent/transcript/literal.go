package transcript

// LiteralDecoder reconstructs one canonical value across bounded records.
// Callers retain record ordering and ownership; no partial messages escape.

import (
	"context"
	"errors"
	"unicode/utf8"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/storage"
)

type (
	// LiteralDecoder joins contiguous literal parts until their final part.
	// The zero value is ready to use. Call Finish before another content kind
	// or the selected history end; a page boundary does not end a literal.
	LiteralDecoder struct {
		data []byte
	}
)

// Append returns messages only after the complete literal decodes successfully.
// Callers must stop using the decoder after an error. Transcript-wide tool
// adjacency is checked after all literal and referenced messages are assembled.
func (d *LiteralDecoder) Append(ctx context.Context, part storage.LiteralPart) ([]*model.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(part.Data) == 0 {
		return nil, errors.New("transcript: literal part requires nonempty data")
	}
	d.data = append(d.data, part.Data...)
	if !part.Final {
		return nil, nil
	}
	data := d.data
	d.data = nil
	if !utf8.Valid(data) {
		return nil, errors.New("transcript: completed literal contains invalid UTF-8")
	}
	messages, err := DecodeRunLogDelta(data)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, errors.New("transcript: completed literal has no messages")
	}
	for _, message := range messages {
		if message == nil {
			return nil, errors.New("transcript: completed literal contains a null message")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

// Finish rejects a transition or history end before the literal's final part.
func (d *LiteralDecoder) Finish() error {
	if len(d.data) != 0 {
		return errors.New("transcript: literal ended before its final part")
	}
	return nil
}
