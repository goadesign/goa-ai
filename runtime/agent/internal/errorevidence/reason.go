package errorevidence

// Rejection records retain the exact text identified by their existing digest.
// The version distinguishes historical digest-only records from current records
// that include bounded UTF-8 reasons and explicitly classify omitted text.

import (
	"fmt"
	"unicode/utf8"
)

const (
	// MaxMessageBytes is the existing per-diagnostic workflow transport allocation.
	// It does not limit the combined diagnostics of an entire run.
	MaxMessageBytes = 3072
	// ReasonVersion identifies exact-or-omitted reason text in rejection records.
	ReasonVersion = "goa_ai.rejection_reason.v1"
)

// RetainedReason returns exact UTF-8 text within the transport allocation. A caller
// records the original digest and size independently, including when omitted.
func RetainedReason(text string) (reason, omitted string) {
	if len(text) > MaxMessageBytes {
		return "", "size_limit"
	}
	if !utf8.ValidString(text) {
		return "", "invalid_utf8"
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
	case ReasonVersion:
		switch omitted {
		case "size_limit":
			if text != "" || size <= MaxMessageBytes {
				return fmt.Errorf("size-limited rejection requires omitted text exceeding the allocation")
			}
			return nil
		case "invalid_utf8":
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
		if !utf8.ValidString(text) {
			return fmt.Errorf("rejection reason is not valid UTF-8")
		}
		actualDigest, actualSize := FingerprintText(text)
		if actualDigest != digest || int64(actualSize) != size {
			return fmt.Errorf("rejection reason does not match its fingerprint")
		}
	default:
		return fmt.Errorf("unsupported rejection reason version %q", version)
	}
	return nil
}

// BoundedMessage retains small diagnostic text and explicitly marks omission
// when it exceeds the allocation or is invalid UTF-8. The original is offered to the
// application tracer separately; a digest is not a retrievable copy.
func BoundedMessage(text string) string {
	retained, omitted := RetainedReason(text)
	if omitted == "" {
		return retained
	}
	digest, size := FingerprintText(text)
	return fmt.Sprintf("diagnostic text omitted (%s; sha256=%s original_bytes=%d)", omitted, digest, size)
}
