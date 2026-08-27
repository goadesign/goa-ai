package model

// This file defines the stable response fingerprint computed at the model
// boundary before a provider-owned response is copied or validated. The
// fingerprint represents malformed raw tool bytes and invalid metadata without
// putting provider content in workflow payloads.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"math"
	"reflect"
	"strconv"

	"goa.design/goa-ai/runtime/agent/internal/responseevidence"
)

const (
	dynamicNull byte = iota
	dynamicBoolean
	dynamicNumber
	dynamicString
	dynamicArray
	dynamicObject
	dynamicBytes
	dynamicStruct
)

type (
	// responseFingerprint is the bounded identity of one complete provider
	// response. Size is the number of bytes encoded into SHA256.
	responseFingerprint struct {
		sha256 string
		size   int64
	}

	responseFingerprintWriter struct {
		hash hash.Hash
		size int64
		walk dynamicValueWalk
	}
)

// fingerprintResponse hashes one complete response before ownership copying or
// validation can reject malformed fields.
func fingerprintResponse(response *Response) (responseFingerprint, error) {
	if err := preflightResponse(response, &dynamicValueWalk{}, dynamicCloneEvidence); err != nil {
		return responseFingerprint{}, err
	}
	return fingerprintPreflightedResponse(response)
}

// fingerprintPreflightedResponse hashes a response after the complete
// response-wide preflight has bounded all traversal and byte work.
func fingerprintPreflightedResponse(response *Response) (responseFingerprint, error) {
	writer := &responseFingerprintWriter{hash: sha256.New()}
	writer.writeString(responseevidence.VersionV2)
	if err := writer.writeResponse(response); err != nil {
		return responseFingerprint{}, err
	}
	return responseFingerprint{
		sha256: hex.EncodeToString(writer.hash.Sum(nil)),
		size:   writer.size,
	}, nil
}

// writeResponse records fields in the stable v2 order.
func (w *responseFingerprintWriter) writeResponse(response *Response) error {
	if response == nil {
		w.writeByte(0)
		return nil
	}
	w.writeByte(1)
	if err := w.writeMessages(response.Content); err != nil {
		return err
	}
	w.writeTokenUsage(response.Usage)
	w.writeString(response.StopReason)
	w.writeBool(response.OutputLimited)
	return nil
}

// writeMessages records ordered messages, their parts, and provider metadata.
func (w *responseFingerprintWriter) writeMessages(messages []Message) error {
	if messages == nil {
		w.writeByte(0)
		return nil
	}
	w.writeByte(1)
	w.writeLength(len(messages))
	for messageIndex, message := range messages {
		w.writeString(string(message.Role))
		if message.Parts == nil {
			w.writeByte(0)
		} else {
			w.writeByte(1)
			w.writeLength(len(message.Parts))
			for partIndex, part := range message.Parts {
				if err := w.writePart(part); err != nil {
					return fmt.Errorf(
						"fingerprint message %d part %d: %w",
						messageIndex,
						partIndex,
						err,
					)
				}
			}
		}
		if err := w.writeDynamicValue(reflect.ValueOf(message.Meta)); err != nil {
			return fmt.Errorf("fingerprint message %d metadata: %w", messageIndex, err)
		}
	}
	return nil
}

// writePart records each closed Part variant with a stable tag.
func (w *responseFingerprintWriter) writePart(part Part) error {
	switch actual := part.(type) {
	case TextPart:
		w.writeString("text")
		w.writeString(actual.Text)
	case ImagePart:
		w.writeString("image")
		w.writeString(string(actual.Format))
		w.writeBytes(actual.Bytes)
	case DocumentPart:
		w.writeString("document")
		w.writeString(actual.Name)
		w.writeString(string(actual.Format))
		w.writeBytes(actual.Bytes)
		w.writeString(actual.Text)
		w.writeStrings(actual.Chunks)
		w.writeString(actual.URI)
		w.writeString(actual.Context)
		w.writeBool(actual.Cite)
	case CitationsPart:
		w.writeString("citations")
		w.writeString(actual.Text)
		if actual.Citations == nil {
			w.writeByte(0)
		} else {
			w.writeByte(1)
			w.writeLength(len(actual.Citations))
			for _, citation := range actual.Citations {
				w.writeString(citation.Title)
				w.writeString(citation.Source)
				w.writeCitationLocation(citation.Location)
				w.writeStrings(citation.SourceContent)
			}
		}
	case ThinkingPart:
		w.writeString("thinking")
		w.writeString(actual.Text)
		w.writeString(actual.Signature)
		w.writeBytes(actual.Redacted)
		w.writeInt(actual.Index)
		w.writeBool(actual.Final)
	case ToolUsePart:
		w.writeString("tool_use")
		w.writeString(actual.ID)
		w.writeString(actual.Name)
		w.writeBytes(actual.Input)
		w.writeString(actual.ThoughtSignature)
	case ToolResultPart:
		w.writeString("tool_result")
		w.writeString(actual.ToolUseID)
		if err := w.writeDynamicValue(reflect.ValueOf(actual.Content)); err != nil {
			return fmt.Errorf("tool result content: %w", err)
		}
		w.writeBool(actual.IsError)
	case CacheCheckpointPart:
		w.writeString("cache_checkpoint")
	case nil:
		w.writeString("nil")
	default:
		return fmt.Errorf("message part type %T is unsupported", part)
	}
	return nil
}

// writeCitationLocation records the three provider-neutral location variants.
func (w *responseFingerprintWriter) writeCitationLocation(location CitationLocation) {
	if location.DocumentChar == nil {
		w.writeByte(0)
	} else {
		w.writeByte(1)
		w.writeInt(location.DocumentChar.DocumentIndex)
		w.writeInt(location.DocumentChar.Start)
		w.writeInt(location.DocumentChar.End)
	}
	if location.DocumentChunk == nil {
		w.writeByte(0)
	} else {
		w.writeByte(1)
		w.writeInt(location.DocumentChunk.DocumentIndex)
		w.writeInt(location.DocumentChunk.Start)
		w.writeInt(location.DocumentChunk.End)
	}
	if location.DocumentPage == nil {
		w.writeByte(0)
	} else {
		w.writeByte(1)
		w.writeInt(location.DocumentPage.DocumentIndex)
		w.writeInt(location.DocumentPage.Start)
		w.writeInt(location.DocumentPage.End)
	}
}

// writeTokenUsage records all provider and logical model usage fields.
func (w *responseFingerprintWriter) writeTokenUsage(usage TokenUsage) {
	w.writeString(usage.Model)
	w.writeString(string(usage.ModelClass))
	w.writeInt(usage.InputTokens)
	w.writeInt(usage.OutputTokens)
	w.writeInt(usage.TotalTokens)
	w.writeInt(usage.CacheReadTokens)
	w.writeInt(usage.CacheWriteTokens)
}

// writeDynamicValue records metadata and tool-result values. String-keyed maps
// are sorted for stable output.
func (w *responseFingerprintWriter) writeDynamicValue(value reflect.Value) error {
	return w.writeDynamicValueAt(value, 0)
}

// writeDynamicValueAt records one value after checking shared recursion and
// work limits.
func (w *responseFingerprintWriter) writeDynamicValueAt(value reflect.Value, depth int) error {
	if !value.IsValid() {
		if _, _, err := w.walk.enter(value, depth); err != nil {
			return err
		}
		w.writeByte(dynamicNull)
		return nil
	}
	if value.Kind() == reflect.Interface {
		if _, _, err := w.walk.enter(value, depth); err != nil {
			return err
		}
		if value.IsNil() {
			w.writeByte(dynamicNull)
			return nil
		}
		return w.writeDynamicValueAt(value.Elem(), depth)
	}
	container, tracked, err := w.walk.enter(value, depth)
	if err != nil {
		return err
	}
	defer w.walk.leave(container, tracked)
	if value.Type() == reflect.TypeOf(json.Number("")) {
		w.writeByte(dynamicNumber)
		w.writeString(value.String())
		return nil
	}
	if value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8 {
		w.writeByte(dynamicBytes)
		if value.IsNil() {
			w.writeBytes(nil)
		} else {
			w.writeBytes(value.Bytes())
		}
		return nil
	}
	switch value.Kind() {
	case reflect.Bool:
		w.writeByte(dynamicBoolean)
		w.writeBool(value.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		w.writeByte(dynamicNumber)
		w.writeString(strconv.FormatInt(value.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		w.writeByte(dynamicNumber)
		w.writeString(strconv.FormatUint(value.Uint(), 10))
	case reflect.Float32:
		w.writeByte(dynamicNumber)
		w.writeUint64(uint64(math.Float32bits(float32(value.Float()))))
	case reflect.Float64:
		w.writeByte(dynamicNumber)
		w.writeUint64(math.Float64bits(value.Float()))
	case reflect.String:
		w.writeByte(dynamicString)
		w.writeString(value.String())
	case reflect.Slice, reflect.Array:
		w.writeByte(dynamicArray)
		if value.Kind() == reflect.Slice && value.IsNil() {
			w.writeByte(0)
			return nil
		}
		w.writeByte(1)
		if err := w.walk.checkChildren(value.Len()); err != nil {
			return err
		}
		w.writeLength(value.Len())
		for index := 0; index < value.Len(); index++ {
			if err := w.writeDynamicValueAt(value.Index(index), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		w.writeByte(dynamicObject)
		if value.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("map key type %s is not a string", value.Type().Key())
		}
		if value.IsNil() {
			w.writeByte(0)
			return nil
		}
		w.writeByte(1)
		if err := w.walk.checkChildren(value.Len()); err != nil {
			return err
		}
		keys := sortedStringMapKeys(value)
		w.writeLength(len(keys))
		for _, key := range keys {
			if err := w.walk.addBytes(len(key.String())); err != nil {
				return err
			}
			w.writeString(key.String())
			if err := w.writeDynamicValueAt(value.MapIndex(key), depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		w.writeByte(dynamicStruct)
		valueType := value.Type()
		if err := w.walk.addBytes(len(valueType.PkgPath()) + len(valueType.Name())); err != nil {
			return err
		}
		w.writeString(valueType.PkgPath())
		w.writeString(valueType.Name())
		if err := w.walk.checkChildren(value.NumField()); err != nil {
			return err
		}
		w.writeLength(value.NumField())
		fieldIndexes := sortedStructFieldIndexes(valueType)
		for _, index := range fieldIndexes {
			field := valueType.Field(index)
			if err := w.walk.addBytes(len(field.Name) + len(field.Tag)); err != nil {
				return err
			}
			w.writeString(field.Name)
			w.writeString(string(field.Tag))
			w.writeBool(field.Anonymous)
			if err := w.writeDynamicValueAt(value.Field(index), depth+1); err != nil {
				return err
			}
		}
	case reflect.Invalid,
		reflect.Interface,
		reflect.Uintptr,
		reflect.Complex64,
		reflect.Complex128,
		reflect.Chan,
		reflect.Func,
		reflect.Pointer,
		reflect.UnsafePointer:
		return fmt.Errorf("value type %s is unsupported", value.Type())
	}
	return nil
}

// nonnegativeFingerprintLength converts lengths after checking the caller's
// invariant.
func nonnegativeFingerprintLength(value int) uint64 {
	if value < 0 {
		panic(fmt.Sprintf("fingerprint collection length is negative: %d", value))
	}
	return uint64(value)
}

// writeLength records a checked collection length.
func (w *responseFingerprintWriter) writeLength(value int) {
	w.writeUint64(nonnegativeFingerprintLength(value))
}

// writeBool records one boolean value.
func (w *responseFingerprintWriter) writeBool(value bool) {
	if value {
		w.writeByte(1)
	} else {
		w.writeByte(0)
	}
}

// writeInt records one signed integer without a narrowing conversion.
func (w *responseFingerprintWriter) writeInt(value int) {
	w.writeString(strconv.FormatInt(int64(value), 10))
}

// writeBytes distinguishes absent, empty, and populated byte slices.
func (w *responseFingerprintWriter) writeBytes(value []byte) {
	if value == nil {
		w.writeByte(0)
		return
	}
	w.writeByte(1)
	w.writeLength(len(value))
	w.write(value)
	w.size += int64(len(value))
}

// writeStrings distinguishes absent, empty, and populated string slices.
func (w *responseFingerprintWriter) writeStrings(values []string) {
	if values == nil {
		w.writeByte(0)
		return
	}
	w.writeByte(1)
	w.writeLength(len(values))
	for _, value := range values {
		w.writeString(value)
	}
}

// writeByte adds one marker byte to the fingerprint.
func (w *responseFingerprintWriter) writeByte(value byte) {
	w.write([]byte{value})
	w.size++
}

// writeUint64 adds one fixed-width number to the fingerprint.
func (w *responseFingerprintWriter) writeUint64(value uint64) {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], value)
	w.write(data[:])
	w.size += int64(len(data))
}

// writeString adds a length-prefixed string to the fingerprint.
func (w *responseFingerprintWriter) writeString(value string) {
	w.writeUint64(nonnegativeFingerprintLength(len(value)))
	w.write([]byte(value))
	w.size += int64(len(value))
}

// write passes bytes to SHA-256, whose standard-library implementation always
// consumes the complete slice and returns no error.
func (w *responseFingerprintWriter) write(value []byte) {
	written, err := w.hash.Write(value)
	if err != nil {
		panic(fmt.Sprintf("write SHA-256 fingerprint: %v", err))
	}
	if written != len(value) {
		panic(fmt.Sprintf("write SHA-256 fingerprint: wrote %d of %d bytes", written, len(value)))
	}
}
