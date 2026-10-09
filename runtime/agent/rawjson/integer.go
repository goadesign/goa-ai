// Package rawjson decodes exact integer JSON values for MCP clients and generated
// codecs. Decimal and exponent spellings retain their mathematical value; the
// requested Go type determines the accepted range without floating-point rounding.
package rawjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// DecodeInteger reads one whole JSON number into the requested Go integer type.
// Decimal and exponent spellings are accepted when their value is whole and fits
// that type. Null, strings, fractional values and out-of-range values fail.
func DecodeInteger[
	T ~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64,
](data []byte) (T, error) {
	var zero T
	text, err := integerText(data)
	if err != nil {
		return zero, err
	}
	// The requested Go type determines its sign. Checking the converted value
	// catches a narrower type's overflow without changing the original number.
	if zero-1 < zero {
		number, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return zero, fmt.Errorf("decode JSON integer: %w", err)
		}
		result := T(number)
		if int64(result) != number {
			return zero, fmt.Errorf("JSON number is outside the requested integer range")
		}
		return result, nil
	}
	number, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return zero, fmt.Errorf("decode JSON integer: %w", err)
	}
	result := T(number)
	if uint64(result) != number {
		return zero, fmt.Errorf("JSON number is outside the requested integer range")
	}
	return result, nil
}

// integerText checks one JSON number and writes its exact whole value as decimal
// digits. It rejects a value outside native integer capacity before expanding an
// exponent, so an extreme exponent cannot cause an oversized allocation.
func integerText(data []byte) (string, error) {
	text := string(bytes.TrimSpace(data))
	if len(text) == 0 || (text[0] != '-' && (text[0] < '0' || text[0] > '9')) || !json.Valid([]byte(text)) {
		return "", fmt.Errorf("expected an integer JSON number")
	}
	negative := text[0] == '-'
	if negative {
		text = text[1:]
	}
	coefficient, exponentText, hasExponent := strings.Cut(text, "e")
	if !hasExponent {
		coefficient, exponentText, hasExponent = strings.Cut(text, "E")
	}
	whole, fraction, _ := strings.Cut(coefficient, ".")
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		return "0", nil
	}
	exponent := 0
	if hasExponent {
		parsed, err := strconv.Atoi(exponentText)
		if err != nil {
			return "", fmt.Errorf("JSON number cannot be represented as an integer")
		}
		exponent = parsed
	}
	// A negative exponent cannot cancel more digits than the input contains.
	// Comparing before subtraction also prevents overflow for extreme exponents.
	if exponent < -len(digits) || exponent > len(fraction)+len(strconv.FormatUint(^uint64(0), 10)) {
		return "", fmt.Errorf("JSON number cannot be represented as an integer")
	}
	scale := exponent - len(fraction)
	if scale < 0 {
		removed := -scale
		if removed >= len(digits) || strings.Trim(digits[len(digits)-removed:], "0") != "" {
			return "", fmt.Errorf("expected a whole JSON number")
		}
		digits = digits[:len(digits)-removed]
		scale = 0
	}
	if len(digits)+scale > len(strconv.FormatUint(^uint64(0), 10)) {
		return "", fmt.Errorf("JSON number cannot be represented as an integer")
	}
	digits += strings.Repeat("0", scale)
	if negative {
		digits = "-" + digits
	}
	return digits, nil
}
