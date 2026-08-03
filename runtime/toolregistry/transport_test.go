package toolregistry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeriveToolUseIDIsStableAndCollisionScoped(t *testing.T) {
	t.Parallel()

	first := DeriveToolUseID("run-a", "call-1")
	assert.Equal(t, first, DeriveToolUseID("run-a", "call-1"))
	assert.NotEqual(t, first, DeriveToolUseID("run-b", "call-1"))
	assert.NotEqual(t, DeriveToolUseID("ab", "c"), DeriveToolUseID("a", "bc"))
	require.NoError(t, ValidateRegistrationToken(first))
}

func TestValidateResultStreamTTLMillis(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		ttlMillis int64
		wantError bool
	}{
		{name: "minimum", ttlMillis: MinResultStreamTTL.Milliseconds()},
		{name: "maximum", ttlMillis: MaxResultStreamTTL.Milliseconds()},
		{name: "below minimum", ttlMillis: MinResultStreamTTL.Milliseconds() - 1, wantError: true},
		{name: "above maximum", ttlMillis: MaxResultStreamTTL.Milliseconds() + 1, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateResultStreamTTLMillis(test.ttlMillis)
			if test.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestDecodeServerDataPreservesTypedPayload(t *testing.T) {
	t.Parallel()

	items, err := DecodeServerData([]byte(`[{"kind":"aura.citations","audience":"evidence","data":[{"index":1}]}]`))
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "aura.citations", items[0].Kind)
	assert.Equal(t, "evidence", items[0].Audience)
	assert.JSONEq(t, `[{"index":1}]`, string(items[0].Data))
}
