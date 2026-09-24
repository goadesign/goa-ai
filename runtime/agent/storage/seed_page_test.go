package storage_test

// Page admission uses the same canonical record codec as append admission.
// These tests pin the framing calculation for history variants and IDs
// whose JSON representation is longer than their source string.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestSeedPageFramingPreservesEveryAcceptedAppend(t *testing.T) {
	for _, runID := range []string{"r", "a-longer-run-identifier", "<&\"\n"} {
		for _, position := range []string{"1", "9", "10", "9223372036854775807"} {
			for _, variant := range []string{"whole", "prefix", "part", "final part"} {
				t.Run(runID+"/"+position+"/"+variant, func(t *testing.T) {
					record := storage.SeedRecord{Key: "<key>", PreviousID: "9223372036854775806"}
					switch variant {
					case "prefix":
						record.Prefix = &storage.HistoryPrefix{RunID: "s", EndID: "e"}
					case "whole":
						record.Messages = rawjson.Message(`[""]`)
					default:
						record.LiteralPart = &storage.LiteralPart{Data: []byte{0}, Final: variant == "final part"}
					}
					appendCommand := storage.SeedAppend{RunID: runID, AttemptID: "a", Record: record}
					base, err := json.Marshal(appendCommand)
					require.NoError(t, err)
					padding := strings.Repeat("x", storage.MaxSeedCommandBytes-len(base))
					switch variant {
					case "prefix":
						appendCommand.Record.Prefix.RunID += padding
					case "whole":
						appendCommand.Record.Messages = rawjson.Message(`["` + padding + `"]`)
					default:
						// Base64 grows four encoded bytes per three source
						// bytes; fill the remaining one to three bytes in Key.
						size := (storage.MaxSeedCommandBytes - len(base) + 4) / 4 * 3
						appendCommand.Record.LiteralPart.Data = make([]byte, size)
						data, err := json.Marshal(appendCommand)
						require.NoError(t, err)
						appendCommand.Record.Key += strings.Repeat("x", storage.MaxSeedCommandBytes-len(data))
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
