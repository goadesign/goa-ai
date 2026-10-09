// Package admission validates saved JSON before binding it to generated types.
// It retains each original consumer contract, including its member order and
// escaping, because those bytes are part of the accepted registration identity.
package admission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	genregistry "goa.design/goa-ai/registry/gen/registry"
)

type (
	// savedUnion uses the selected value returned by a generated Goa union.
	// Its JSON decoder chooses the branch before saved member names are checked.
	savedUnion interface {
		json.Unmarshaler
		Value() (any, error)
	}

	// savedDeclarationReader checks saved JSON values and records slices
	// of the input for consumer contracts in the order of their tool declarations.
	savedDeclarationReader struct {
		raw       []byte
		decoder   *json.Decoder
		contracts []json.RawMessage
	}
)

// SavedToolsetFingerprint strictly decodes a saved definition and returns its
// identity using the original consumer contract bytes. Missing and null contracts
// contribute no bytes, matching a nil contract in current typed registrations.
func SavedToolsetFingerprint(raw []byte) (*genregistry.Toolset, string, error) {
	toolset, contracts, err := parseSavedDeclaration(raw)
	if err != nil {
		return nil, "", err
	}
	// A null definition has no identity; the registry rejects it as an invalid
	// static definition alongside its existing name and registration-time checks.
	if toolset == nil {
		return nil, "", nil
	}
	fingerprint, err := declarationFingerprint(toolset, contracts)
	return toolset, fingerprint, err
}

// parseSavedDeclaration rejects ambiguous keys before typed decoding and keeps
// raw contracts in the same tool order as the resulting generated declaration.
func parseSavedDeclaration(raw []byte) (*genregistry.Toolset, []json.RawMessage, error) {
	reader := savedDeclarationReader{raw: raw, decoder: json.NewDecoder(bytes.NewReader(raw))}
	if err := reader.readValue(reflect.TypeFor[genregistry.Toolset](), nil); err != nil {
		return nil, nil, err
	}
	if _, err := reader.decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("trailing JSON value")
	}
	// The structural read has checked names and the end of the document. Bind
	// values once so generated types still decide which value encodings are valid.
	var toolset *genregistry.Toolset
	if err := json.Unmarshal(raw, &toolset); err != nil {
		return nil, nil, err
	}
	return toolset, reader.contracts, nil
}

// savedMemberType checks an object key against its generated Go representation.
// Application map keys remain case-sensitive data. A nil target means the typed
// decoder must reject the containing value's shape after structural checks.
func savedMemberType(target reflect.Type, name string) (reflect.Type, error) {
	if target == nil {
		return nil, nil
	}
	if target.Kind() == reflect.Struct {
		for i := range target.NumField() {
			field := target.Field(i)
			if !field.IsExported() {
				continue
			}
			fieldName, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if fieldName == "-" {
				continue
			}
			if fieldName == "" {
				fieldName = field.Name
			}
			if fieldName == name {
				return field.Type, nil
			}
		}
		return nil, fmt.Errorf("unknown field %q in %s; saved member names must match generated JSON names exactly", name, target.Name())
	}
	if target.Kind() == reflect.Map {
		return target.Elem(), nil
	}
	return nil, nil
}

// readValue consumes one JSON value, rejects duplicate and unknown object keys,
// and saves the original object bytes when that value is a consumer contract.
// Scalar encodings and null values are left to the generated typed decoder.
func (r *savedDeclarationReader) readValue(target reflect.Type, contract *json.RawMessage) error {
	for target != nil && target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target != nil && reflect.PointerTo(target).Implements(reflect.TypeFor[savedUnion]()) {
		union := reflect.New(target).Interface().(savedUnion)
		return r.readUnion(target, union, contract)
	}
	token, err := r.decoder.Token()
	if err != nil {
		return err
	}
	return r.readTokenValue(target, contract, token)
}

// readTokenValue checks the value whose opening token has already been read.
// Object offsets begin at that token, excluding surrounding colons and commas.
func (r *savedDeclarationReader) readTokenValue(target reflect.Type, contract *json.RawMessage, token json.Token) error {
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	// InputOffset points just after the opening delimiter, excluding any colon,
	// comma or whitespace before it. The closing offset preserves the object's
	// member order, internal whitespace and string escaping without a copy.
	start := r.decoder.InputOffset() - 1
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for r.decoder.More() {
			token, err := r.decoder.Token()
			if err != nil {
				return err
			}
			name, ok := token.(string)
			if !ok {
				return fmt.Errorf("object member name is not a string")
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("duplicate JSON member %q", name)
			}
			seen[name] = struct{}{}
			field, err := savedMemberType(target, name)
			if err != nil {
				return err
			}
			if err := r.readValue(field, contract); err != nil {
				return err
			}
		}
	case '[':
		var element reflect.Type
		if target != nil && (target.Kind() == reflect.Slice || target.Kind() == reflect.Array) {
			element = target.Elem()
		}
		for r.decoder.More() {
			var body json.RawMessage
			if err := r.readValue(element, &body); err != nil {
				return err
			}
			if target == reflect.TypeFor[[]*genregistry.ToolSchema]() {
				// Every tool occupies one position, including tools whose
				// contract is absent or null, so hashing uses the right bytes.
				r.contracts = append(r.contracts, body)
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	if _, err := r.decoder.Token(); err != nil {
		return err
	}
	if delimiter == '{' && target == reflect.TypeFor[genregistry.ConsumerContract]() {
		*contract = r.raw[start:r.decoder.InputOffset()]
	}
	return nil
}

// readUnion checks duplicate keys, asks Goa to decode the selected branch, and
// then checks exact saved member names against that branch's generated type.
// Both reads use slices of the original input, preserving registration identity.
func (r *savedDeclarationReader) readUnion(target reflect.Type, union savedUnion, contract *json.RawMessage) error {
	token, err := r.decoder.Token()
	if err != nil {
		return err
	}
	start := r.decoder.InputOffset() - 1
	if token != json.Delim('{') {
		return fmt.Errorf("saved %s must be a union object", target.Name())
	}
	if err := r.readTokenValue(nil, contract, token); err != nil {
		return err
	}
	raw := r.raw[start:r.decoder.InputOffset()]
	if err := union.UnmarshalJSON(raw); err != nil {
		return err
	}
	value, err := union.Value()
	if err != nil {
		return err
	}
	// Goa unions save two envelope members. The value's generated Go type
	// supplies the exact names inside its selected branch, including nested unions.
	envelope := reflect.StructOf([]reflect.StructField{
		{Name: "Type", Type: reflect.TypeFor[string](), Tag: `json:"type"`},
		{Name: "Value", Type: reflect.TypeOf(value), Tag: `json:"value"`},
	})
	reader := savedDeclarationReader{raw: raw, decoder: json.NewDecoder(bytes.NewReader(raw))}
	if err := reader.readValue(envelope, contract); err != nil {
		return fmt.Errorf("decode saved %s: %w", target.Name(), err)
	}
	return nil
}
