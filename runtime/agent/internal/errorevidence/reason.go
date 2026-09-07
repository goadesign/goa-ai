package errorevidence

// Rejection records retain the exact text identified by their existing digest.
// The version distinguishes historical digest-only records from current records
// that include UTF-8 reasons and explicitly identify invalid source text.

import (
	"fmt"
	"unicode/utf8"
)

const (
	// MaxMessageBytes is the allocation enforced by historical v1 records only.
	MaxMessageBytes = 3072
	// LegacyReasonVersion identifies the historical length-limited format.
	LegacyReasonVersion = "goa_ai.rejection_reason.v1"
	// ReasonVersion identifies exact valid UTF-8 text without a per-reason limit.
	ReasonVersion = "goa_ai.rejection_reason.v2"

	reasonSizeLimit   = "size_limit"
	reasonInvalidUTF8 = "invalid_utf8"
)

// RetainedReason returns exact UTF-8 text without a per-message allocation. A caller
// records the original digest and size independently, including when omitted.
func RetainedReason(text string) (reason, omitted string) {
	if !utf8.ValidString(text) {
		return "", reasonInvalidUTF8
	}
	return text, ""
}

// ValidateReason checks a saved reason against its version and original digest.
// Legacy records must remain text-free so replay preserves historical bytes.
func ValidateReason(version, text, omitted, digest string, size int64) error {
	switch version {
	case "":
		if text != "" || omitted != "" {
			return fmt.Errorf("legacy rejection contains reason text")
		}
	case LegacyReasonVersion:
		switch omitted {
		case reasonSizeLimit:
			if text != "" || size <= MaxMessageBytes {
				return fmt.Errorf("size-limited rejection requires omitted text exceeding the allocation")
			}
			return nil
		case reasonInvalidUTF8:
			if text != "" || size <= 0 || size > MaxMessageBytes {
				return fmt.Errorf("invalid UTF-8 rejection requires omitted nonempty bytes within the allocation")
			}
			// Omitted bytes cannot be revalidated here; the writer asserts the
			// encoding failure and retains their original fingerprint.
			return nil
		case "":
			if size > MaxMessageBytes {
				return fmt.Errorf("oversized rejection reason requires size_limit omission")
			}
		default:
			return fmt.Errorf("unsupported rejection reason omission %q", omitted)
		}
		return validateExactReason(text, digest, size)
	case ReasonVersion:
		switch omitted {
		case reasonInvalidUTF8:
			if text != "" || size <= 0 {
				return fmt.Errorf("invalid UTF-8 rejection requires omitted nonempty bytes")
			}
			return nil
		case "":
			return validateExactReason(text, digest, size)
		default:
			return fmt.Errorf("unsupported rejection reason omission %q", omitted)
		}
	default:
		return fmt.Errorf("unsupported rejection reason version %q", version)
	}
	return nil
}

// BoundedMessage preserves historical Temporal wrapping, including the old
// length allocation. New failures use DiagnosticMessage instead.
func BoundedMessage(text string) string {
	retained, omitted := RetainedReason(text)
	if len(text) > MaxMessageBytes {
		retained, omitted = "", reasonSizeLimit
	}
	if omitted == "" {
		return retained
	}
	digest, size := FingerprintText(text)
	return fmt.Sprintf("diagnostic text omitted (%s; sha256=%s original_bytes=%d)", omitted, digest, size)
}

// DiagnosticMessage preserves exact valid text. Invalid UTF-8 is explicitly
// unavailable because JSON and protobuf strings cannot preserve those bytes.
func DiagnosticMessage(text string) string {
	if utf8.ValidString(text) {
		return text
	}
	digest, size := FingerprintText(text)
	return fmt.Sprintf("diagnostic text omitted (invalid_utf8; sha256=%s original_bytes=%d)", digest, size)
}

// validateExactReason verifies saved text against the original fingerprint.
func validateExactReason(text, digest string, size int64) error {
	if !utf8.ValidString(text) {
		return fmt.Errorf("rejection reason is not valid UTF-8")
	}
	actualDigest, actualSize := FingerprintText(text)
	if actualDigest != digest || int64(actualSize) != size {
		return fmt.Errorf("rejection reason does not match its fingerprint")
	}
	return nil
}
