package storage

// Fragment bytes are opaque, but their JSON envelope is a strict contract.

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLiteralPartRequiredFields(t *testing.T) {
	for _, input := range []string{
		`{}`, `null`, `{"Data":"eA=="}`, `{"Final":false}`,
		`{"Data":null,"Final":true}`, `{"Data":"","Final":false}`,
		`{"Data":"eA==","Final":null}`, `{"Data":"eA==","Final":0}`,
		`{"Data":"eA==","Final":false,"Extra":1}`,
		`{"Data":"eA==","Final":false} {}`,
		"{\"Data\":\"\xff\",\"Final\":true}",
	} {
		t.Run(input, func(t *testing.T) {
			var part LiteralPart
			require.Error(t, json.Unmarshal([]byte(input), &part))
		})
	}
	for _, final := range []bool{false, true} {
		part := LiteralPart{Data: []byte{0xff, 0, 0xe2}, Final: final}
		encoded, err := json.Marshal(part)
		require.NoError(t, err)
		require.Contains(t, string(encoded), `"Final":`)
		var decoded LiteralPart
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		require.Equal(t, part, decoded)
	}
}

func TestSeedLiteralVariantAndWholeEncoding(t *testing.T) {
	record := SeedRecord{Key: "first", PreviousID: EmptySeedEndID, Messages: []byte(`[]`)}
	encoded, err := json.Marshal(record)
	require.NoError(t, err)
	require.True(t, bytes.Equal([]byte(`{"ID":"","Key":"first","PreviousID":"0","Messages":[],"Prefix":null,"Prepared":null}`), encoded),
		"old whole literals keep their exact bytes and field order")
	command := SeedAppend{RunID: "run", AttemptID: "attempt", Record: record}
	require.NoError(t, ValidateSeedAppend(command))
	command.Record.LiteralPart = &LiteralPart{Data: []byte("x")}
	require.Error(t, ValidateSeedAppend(command))
	command.Record.Messages = nil
	require.NoError(t, ValidateSeedAppend(command))
	command.Record.LiteralPart.Data = nil
	require.Error(t, ValidateSeedAppend(command))
}
