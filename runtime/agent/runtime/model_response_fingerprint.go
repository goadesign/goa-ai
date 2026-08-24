package runtime

// This file hashes bounded local rejection errors and validates fingerprints
// before they cross the workflow boundary.

import (
	"crypto/sha256"
	"encoding/hex"
)

// fingerprintBytes returns a fixed-size identity and the exact input length.
func fingerprintBytes(value []byte) (string, int64) {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:]), int64(len(value))
}

// validFingerprint reports whether a bounded digest and size form one complete
// identity. Optional fingerprints use empty digest and zero size for absence.
func validFingerprint(digest string, size int64, optional bool) bool {
	if digest == "" {
		return optional && size == 0
	}
	decoded, err := hex.DecodeString(digest)
	return err == nil &&
		len(decoded) == sha256.Size &&
		hex.EncodeToString(decoded) == digest &&
		size > 0
}

// validReasonFingerprint accepts the digest of an empty local error because an
// error may legally return an empty string.
func validReasonFingerprint(digest string, size int64) bool {
	decoded, err := hex.DecodeString(digest)
	if err != nil ||
		len(decoded) != sha256.Size ||
		hex.EncodeToString(decoded) != digest ||
		size < 0 {
		return false
	}
	if size == 0 {
		empty := sha256.Sum256(nil)
		return digest == hex.EncodeToString(empty[:])
	}
	return true
}
