package bedrock

// tool_name_diagnostics.go provides compact, high-signal diagnostics for
// Bedrock tool name translation failures.
//
// Contract:
// - Diagnostics must be safe to include in errors that may surface in Temporal
//   histories and logs.
// - Keep output bounded and deterministic (sorted, truncated samples).

import (
	"fmt"
	"sort"
	"strings"
)

const reverseMapSampleLimit = 12

func reverseToolNameDiagnostics(nameMap map[string]string, raw string) string {
	normalized := normalizeToolName(raw)
	if nameMap == nil {
		return fmt.Sprintf("reverse_map=nil raw=%q normalized=%q", raw, normalized)
	}

	keys := make([]string, 0, len(nameMap))
	values := make([]string, 0, len(nameMap))
	for k, v := range nameMap {
		keys = append(keys, k)
		values = append(values, v)
	}
	sort.Strings(keys)
	sort.Strings(values)

	keySample, keysTruncated := sampleStrings(keys, reverseMapSampleLimit)
	valSample, valsTruncated := sampleStrings(values, reverseMapSampleLimit)

	var b strings.Builder
	b.WriteString("raw=")
	b.WriteString(strconvQuote(raw))
	b.WriteString(" normalized=")
	b.WriteString(strconvQuote(normalized))

	if strings.HasPrefix(raw, "$FUNCTIONS.") {
		b.WriteString(" functions_prefix=true")
	}

	sanitized := SanitizeToolName(normalized)
	if sanitized != normalized {
		b.WriteString(" sanitized(normalized)=")
		b.WriteString(strconvQuote(sanitized))
	}
	if canonical, ok := nameMap[sanitized]; ok {
		b.WriteString(" sanitized_in_reverse_map=true canonical=")
		b.WriteString(strconvQuote(canonical))
	}
	if strings.Contains(normalized, ".") {
		b.WriteString(" normalized_looks_canonical=true expected_sanitized=")
		b.WriteString(strconvQuote(sanitized))
	}

	b.WriteString(" expected_sanitized_count=")
	b.WriteString(fmt.Sprintf("%d", len(keys)))
	if len(keySample) > 0 {
		b.WriteString(" expected_sanitized_sample=[")
		b.WriteString(strings.Join(keySample, ","))
		if keysTruncated {
			b.WriteString(",…")
		}
		b.WriteString("]")
	}

	b.WriteString(" expected_canonical_count=")
	b.WriteString(fmt.Sprintf("%d", len(values)))
	if len(valSample) > 0 {
		b.WriteString(" expected_canonical_sample=[")
		b.WriteString(strings.Join(valSample, ","))
		if valsTruncated {
			b.WriteString(",…")
		}
		b.WriteString("]")
	}

	return b.String()
}

func sampleStrings(in []string, limit int) (out []string, truncated bool) {
	if len(in) == 0 {
		return nil, false
	}
	if limit <= 0 || len(in) <= limit {
		out = make([]string, len(in))
		copy(out, in)
		for i := range out {
			out[i] = strconvQuote(out[i])
		}
		return out, false
	}
	out = make([]string, limit)
	copy(out, in[:limit])
	for i := range out {
		out[i] = strconvQuote(out[i])
	}
	return out, true
}

func strconvQuote(s string) string {
	// Use a tiny local wrapper to avoid importing strconv in multiple files for a
	// single Quote call; this keeps diagnostics readable and consistent.
	return fmt.Sprintf("%q", s)
}

