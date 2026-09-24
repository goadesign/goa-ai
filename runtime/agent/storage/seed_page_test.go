package storage_test

// Page admission uses the same canonical record codec as append admission.
// These tests pin the framing calculation for both record variants and IDs
// whose JSON representation is longer than their source string.

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestSeedPageFramingPreservesEveryAcceptedAppend(t *testing.T) {
	for _, runID := range []string{"r", "a-longer-run-identifier", "<&\"\n"} {
		for _, position := range []string{"1", "9", "10", "9223372036854775807"} {
			for _, prefix := range []bool{false, true} {
				t.Run(runID+"/"+position+"/"+strconv.FormatBool(prefix), func(t *testing.T) {
					record := storage.SeedRecord{Key: "<key>", PreviousID: "9223372036854775806"}
					if prefix {
						record.Prefix = &storage.HistoryPrefix{RunID: "s", EndID: "e"}
					} else {
						record.Messages = rawjson.Message(`[""]`)
					}
					appendCommand := storage.SeedAppend{RunID: runID, AttemptID: "a", Record: record}
					base, err := json.Marshal(appendCommand)
					require.NoError(t, err)
					padding := strings.Repeat("x", storage.MaxSeedCommandBytes-len(base))
					if prefix {
						appendCommand.Record.Prefix.RunID += padding
					} else {
						appendCommand.Record.Messages = rawjson.Message(`["` + padding + `"]`)
					}
					require.NoError(t, storage.ValidateSeedAppend(appendCommand))
					encodedAppend, err := json.Marshal(appendCommand)
					require.NoError(t, err)
					require.Len(t, encodedAppend, storage.MaxSeedCommandBytes)
					stored := appendCommand.Record
					stored.ID = position
					page := storage.SeedPage{Records: []storage.SeedRecord{stored}, NextCursor: position}
					encodedPage, err := json.Marshal(page)
					require.NoError(t, err)
					encodedRunID, err := json.Marshal(runID)
					require.NoError(t, err)
					encodedAttemptID, err := json.Marshal(appendCommand.AttemptID)
					require.NoError(t, err)
					extra := 8 + 2*len(position) - (len(encodedRunID) - 2) - len(`,"AttemptID":`) - len(encodedAttemptID)
					require.Len(t, encodedPage, len(encodedAppend)+extra)
					require.NoError(t, storage.ValidateSeedPageSize(page))
					if runID == "r" && len(position) == 19 {
						page.Records[0].Key += strings.Repeat("x", storage.MaxSeedPageBytes-len(encodedPage))
						require.NoError(t, storage.ValidateSeedPageSize(page))
						page.Records[0].Key += "x"
						require.Error(t, storage.ValidateSeedPageSize(page))
					}
				})
			}
		}
	}
}
