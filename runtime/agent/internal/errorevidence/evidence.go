// Package errorevidence extracts bounded evidence from untrusted errors.
//
// Application errors cross activity and workflow boundaries. Their Error
// methods may panic or return very large strings, so callers use this package
// instead of invoking Error directly or copying the complete text to bytes.
package errorevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
)

const unavailableText = "error message unavailable"

// Text returns err's text and converts a panicking Error method to a fixed
// message. A nil error has empty text.
func Text(err error) (text string) {
	if err == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			text = unavailableText
		}
	}()
	return err.Error()
}

// Fingerprint returns the SHA-256 digest and byte length of panic-safe error
// text. It writes the string directly to the hash without making a full byte
// copy.
func Fingerprint(err error) (string, int) {
	return FingerprintText(Text(err))
}

// FingerprintText returns the SHA-256 digest and byte length of text without
// making a full byte copy.
func FingerprintText(text string) (string, int) {
	hash := sha256.New()
	if _, err := io.WriteString(hash, text); err != nil {
		panic("errorevidence: SHA-256 write failed")
	}
	return hex.EncodeToString(hash.Sum(nil)), len(text)
}
