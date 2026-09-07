package errorevidence

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReasonFormatRetainsExactText(t *testing.T) {
	for _, reason := range []string{"", "field[7]: invalid value", strings.Repeat("é", MaxMessageBytes/2), strings.Repeat("é", MaxMessageBytes/2) + "x", string([]byte{0xff}), strings.Repeat("x", MaxMessageBytes) + string([]byte{0xff})} {
		digest, size := FingerprintText(reason)
		retained, omitted := RetainedReason(reason)
		require.NoError(t, ValidateReason(ReasonVersion, retained, omitted, digest, int64(size)))
		if omitted == "" {
			assert.Equal(t, reason, retained)
		} else {
			assert.Empty(t, retained)
			assert.Contains(t, BoundedMessage(reason), "omitted")
			assert.Equal(t, reasonInvalidUTF8, omitted)
		}
		assert.True(t, utf8.ValidString(BoundedMessage(reason)))
		assert.LessOrEqual(t, len(BoundedMessage(reason)), MaxMessageBytes)
	}
}

func TestReasonFormatRejectsMixedAndForgedRecords(t *testing.T) {
	digest, size := FingerprintText("reason")
	require.NoError(t, ValidateReason("", "", "", digest, int64(size)))
	require.Error(t, ValidateReason("", "reason", "", digest, int64(size)))
	require.Error(t, ValidateReason("", "", reasonInvalidUTF8, digest, int64(size)))
	require.Error(t, ValidateReason("unknown", "reason", "", digest, int64(size)))
	require.Error(t, ValidateReason(ReasonVersion, "", "", digest, int64(size)))
	require.Error(t, ValidateReason(ReasonVersion, "different", "", digest, int64(size)))
	require.Error(t, ValidateReason(ReasonVersion, "reason", "", digest, MaxMessageBytes+1))
	require.Error(t, ValidateReason(ReasonVersion, string([]byte{0xff}), "", digest, 1))
	require.Error(t, ValidateReason(ReasonVersion, "", "unknown", digest, 1))
	require.Error(t, ValidateReason(ReasonVersion, "", reasonSizeLimit, digest, MaxMessageBytes))
	require.Error(t, ValidateReason(ReasonVersion, "x", reasonSizeLimit, digest, MaxMessageBytes+1))
	require.NoError(t, ValidateReason(ReasonVersion, "", reasonInvalidUTF8, digest, MaxMessageBytes+1))
	require.Error(t, ValidateReason(ReasonVersion, "", reasonInvalidUTF8, digest, 0))
	require.Error(t, ValidateReason(ReasonVersion, "x", reasonInvalidUTF8, digest, 1))
}

func TestHistoricalReasonVersionKeepsLengthAndEncodingRules(t *testing.T) {
	for _, reason := range []string{"", "reason", strings.Repeat("x", MaxMessageBytes), strings.Repeat("x", MaxMessageBytes+1), string([]byte{0xff}), strings.Repeat("x", MaxMessageBytes) + string([]byte{0xff})} {
		digest, size := FingerprintText(reason)
		retained, omitted := reason, ""
		switch {
		case size > MaxMessageBytes:
			retained, omitted = "", reasonSizeLimit
		case !utf8.ValidString(reason):
			retained, omitted = "", reasonInvalidUTF8
		}
		require.NoError(t, ValidateReason(LegacyReasonVersion, retained, omitted, digest, int64(size)))
	}
	digest, size := FingerprintText(strings.Repeat("x", MaxMessageBytes+1))
	require.Error(t, ValidateReason(LegacyReasonVersion, strings.Repeat("x", size), "", digest, int64(size)))
	require.Error(t, ValidateReason(LegacyReasonVersion, "", reasonInvalidUTF8, digest, int64(size)))
	require.Error(t, ValidateReason(ReasonVersion, "", reasonSizeLimit, digest, int64(size)))
}

func TestDiagnosticAllocationIsNotRunWide(t *testing.T) {
	reason := strings.Repeat("x", MaxMessageBytes)
	for range 20 {
		digest, size := FingerprintText(reason)
		retained, omitted := RetainedReason(reason)
		require.NoError(t, ValidateReason(ReasonVersion, retained, omitted, digest, int64(size)))
		assert.Equal(t, reason, BoundedMessage(reason))
	}
}
