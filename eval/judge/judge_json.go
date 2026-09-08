// Package judge requires unique JSON member names in the raw reply: normal
// map decoding silently retains only the last value, hiding conflicting model
// decisions. JSON Schema cannot express this rule after parsing. This check is
// local to the judge codec and returns a correctable tool validation error.
package judge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"goa.design/goa-ai/runtime/agent/tools"
)

func validateMemberNames(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return readUniqueMembers(decoder, "$")
}

// readUniqueMembers consumes one JSON value, comparing decoded names within
// each object. In particular, "label" and "\u006cabel" name the same member.
func readUniqueMembers(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := make(map[string]struct{})
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			// Decoder.Token guarantees string member names inside an object.
			name := token.(string)
			field := path + "[" + strconv.Quote(name) + "]"
			if _, exists := seen[name]; exists {
				return tools.NewValidationError("duplicate JSON member "+field, []*tools.FieldIssue{{
					Field:      field,
					Constraint: "invalid_format",
					Format:     "unique JSON object member names",
				}}, nil)
			}
			seen[name] = struct{}{}
			if err := readUniqueMembers(decoder, field); err != nil {
				return err
			}
		}
	case json.Delim('['):
		for index := 0; decoder.More(); index++ {
			if err := readUniqueMembers(decoder, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	default:
		return nil
	}
	_, err = decoder.Token()
	return err
}
