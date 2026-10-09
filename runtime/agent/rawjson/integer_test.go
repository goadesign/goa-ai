// These checks cover equivalent numeric spellings and the range chosen by the
// caller's Go type. A value rejected by a narrow type remains valid for a wider one.
package rawjson

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testCount int32

func TestDecodeIntegerExactValues(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  int64
	}{
		{"3", 3}, {"3.0", 3}, {"3e0", 3}, {"300e-2", 3}, {"0.003e3", 3},
		{"-0.0", 0}, {"0e999999999999999999999999", 0},
		{"9223372036854775807.0", 9223372036854775807},
		{"-9223372036854775808e0", -9223372036854775808},
	} {
		t.Run(tc.input, func(t *testing.T) {
			value, err := DecodeInteger[int64]([]byte(tc.input))
			require.NoError(t, err)
			assert.Equal(t, tc.want, value)
		})
	}
	value, err := DecodeInteger[testCount]([]byte("3e0"))
	require.NoError(t, err)
	assert.Equal(t, testCount(3), value)
}

func TestDecodeIntegerRejectsInvalidValues(t *testing.T) {
	for _, input := range []string{"", "null", "\"3\"", "true", "{}", "[]", "3 4", "01", "3.5", "3e-1", "1e9999999999999999999", "1e-9999999999999999999", "9223372036854775808", "-9223372036854775809"} {
		t.Run(input, func(t *testing.T) {
			_, err := DecodeInteger[int64]([]byte(input))
			assert.Error(t, err)
		})
	}
}

func TestDecodeIntegerTypeRanges(t *testing.T) {
	for _, tc := range []struct {
		input string
		valid bool
	}{
		{"-2147483649", false}, {"-2147483648", true}, {"2147483647", true}, {"2147483648", false},
	} {
		t.Run("int32/"+tc.input, func(t *testing.T) {
			_, err := DecodeInteger[int32]([]byte(tc.input))
			assert.Equal(t, tc.valid, err == nil)
			_, widerErr := DecodeInteger[int64]([]byte(tc.input))
			assert.NoError(t, widerErr)
		})
	}
	for _, tc := range []struct {
		input string
		valid bool
	}{
		{"-1", false}, {"0", true}, {"18446744073709551615.0", true}, {"18446744073709551616", false},
	} {
		t.Run("uint64/"+tc.input, func(t *testing.T) {
			_, err := DecodeInteger[uint64]([]byte(tc.input))
			assert.Equal(t, tc.valid, err == nil)
		})
	}
	_, err := DecodeInteger[int8]([]byte("128"))
	require.Error(t, err)
	_, err = DecodeInteger[uint8]([]byte("256"))
	require.Error(t, err)
	_, err = DecodeInteger[int16]([]byte("32768"))
	require.Error(t, err)
	_, err = DecodeInteger[uint16]([]byte("65536"))
	require.Error(t, err)
	_, err = DecodeInteger[uint32]([]byte("4294967296"))
	require.Error(t, err)
	_, err = DecodeInteger[int]([]byte("3.0"))
	require.NoError(t, err)
	_, err = DecodeInteger[uint]([]byte("3e0"))
	require.NoError(t, err)
}
