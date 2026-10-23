// These tests keep saved registration identities stable when the fingerprint
// writer changes. Expected hashes come from the original compact JSON format.
package admission

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSchemaFingerprintPreservesSavedIdentities(t *testing.T) {
	empty := ""
	text := "<html>&\"\\\n雪\u2028\u2029"
	invalidUTF8 := string([]byte{'x', 0xff, 'y'})
	tool := ToolSchema{
		Name:                   "records.read",
		Tags:                   []string{"z", "a", "a"},
		PayloadSchema:          []byte(`{"type":"object"}`),
		ExecutionPayloadSchema: []byte(`{"type":"object"}`),
		ResultSchema:           []byte(`{"type":"string"}`),
	}
	ordered := Schema{
		Name: "records",
		Tags: []string{"z", "a"},
		Tools: []ToolSchema{
			tool,
			{Name: "records.a", Tags: []string{}, SidecarSchema: []byte(`{"type":"null"}`)},
		},
	}
	reversed := ordered
	reversed.Tags = slices.Clone(ordered.Tags)
	slices.Reverse(reversed.Tags)
	reversed.Tools = slices.Clone(ordered.Tools)
	slices.Reverse(reversed.Tools)

	for _, test := range []struct {
		name   string
		schema Schema
		want   string
	}{
		{
			name: "absent collections",
			want: "f148d29ee9c93ef2412e9b7268ef77133da661ec21c4802a60ce72a1897257b9",
		},
		{
			name:   "empty collections",
			schema: Schema{Tags: []string{}, Tools: []ToolSchema{}},
			want:   "f148d29ee9c93ef2412e9b7268ef77133da661ec21c4802a60ce72a1897257b9",
		},
		{
			name:   "present empty strings",
			schema: Schema{Name: "records", Description: &empty, Version: &empty},
			want:   "d89479b69acdf737e063c84f50a0a38b563e44523861229024be860d5b6ccd71",
		},
		{
			name: "escaped text",
			schema: Schema{
				Name: text, Description: &text, Version: &text, Tags: []string{text, "a"},
				Tools: []ToolSchema{{Name: text, Description: &text, ConsumerContract: []byte(text)}},
			},
			want: "4229acd9fef33e55aedac4c1845c9584900c9bf1796841cf9a2a2d6c0aa89047",
		},
		{
			name: "legacy invalid UTF8 encoding",
			schema: Schema{
				Name: invalidUTF8, Description: &invalidUTF8, Tags: []string{invalidUTF8},
				Tools: []ToolSchema{{Name: invalidUTF8, PayloadSchema: []byte{0xff}, ConsumerContract: []byte{0xff}}},
			},
			want: "e1185914a5526d2a003055999903852c686fef8b68e8450a07345f0e1a2772ff",
		},
		{
			name:   "tool and tag ordering",
			schema: ordered,
			want:   "40c037cbe9d1e08ba08e1c1251b893cc9834167e8203c3c79a64ee6456e50275",
		},
		{
			name:   "reversed tool and tag ordering",
			schema: reversed,
			want:   "40c037cbe9d1e08ba08e1c1251b893cc9834167e8203c3c79a64ee6456e50275",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, SchemaFingerprint(test.schema))
		})
	}

	// Saved contract bytes enter the identity unchanged, including their original
	// spacing and escaping; decoding them with current types would change hashes.
	for _, test := range []struct {
		name     string
		contract string
		want     string
	}{
		{
			name:     "compact contract",
			contract: `{"Kind":"service","Title":"Read records"}`,
			want:     "a4757425f9aa184a7586e4852c13d0238a6ef52d88a6e7ac863dc8cdd44ed8c5",
		},
		{
			name:     "original spacing and escaping",
			contract: `{ "Title" : "Read \u0072ecords", "Kind" : "service" }`,
			want:     "df12793b66c3d7a9bad9cf8270345098541549d991cd2c3515b80f704390ab6d",
		},
		{
			name:     "historical contract fields",
			contract: `{"Kind":"service","Bounds":{"Paging":{"ReplayPayload":true}}}`,
			want:     "730c5caae33aa4fc5a5243a0535e481594d2b103235bd1d68b11f8968132d72a",
		},
		{
			name:     "field union",
			contract: `{"Kind":"service","Payload":{"Fields":[{"Path":[{"Segment":{ "value" : "record", "type" : "field" }}]}]}}`,
			want:     "44d08cfbad3da8e6fbc8050f1d500c8183ff41295f933af50cebef7628ec046b",
		},
		{
			name:     "element union",
			contract: `{"Kind":"service","Payload":{"Fields":[{"Path":[{"Segment":{"type":"element","value":{}}}]}]}}`,
			want:     "66b7765a6112a15383145b666e7f81dfed1a26f7e2390f9db89dce03d47651b3",
		},
		{
			name:     "literal null contract bytes",
			contract: `null`,
			want:     "42aff3a46383d183e76c9375d5fa6e52934e9bb85cdc6dbb900c9e163e2e722f",
		},
		{
			name: "absent contract bytes",
			want: "4f542f022576c85f0d431b67ed3d5b29d86b9ccfd19b44e4c885da66fbf609fa",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			declaration := tool
			declaration.ConsumerContract = []byte(test.contract)
			assert.Equal(t, test.want, SchemaFingerprint(Schema{Name: "records", Tools: []ToolSchema{declaration}}))
		})
	}
}
