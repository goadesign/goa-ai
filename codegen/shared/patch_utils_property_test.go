package shared

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// TestPatchFailureDetectionProperty verifies Property 17: Template Patch Failure Detection.
// *For any* expected template pattern that is not found during patching, the system
// should return a clear error rather than silently producing incorrect code.
func TestPatchFailureDetectionProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("ReplaceOnce returns error when pattern not found", prop.ForAll(
		func(pair stringPair) bool {
			source, pattern := pair.Source, pair.Pattern
			// Ensure pattern is not in source
			if strings.Contains(source, pattern) {
				return true // Skip this case - pattern exists
			}

			_, err := ReplaceOnce(source, pattern, "replacement", "test.go", "test context")
			if err == nil {
				return false // Should have returned an error
			}

			var patchErr *PatchError
			if !errors.As(err, &patchErr) {
				return false // Should be a PatchError
			}

			// Verify error contains useful information
			return patchErr.File == "test.go" && patchErr.Context == "test context"
		},
		genNonOverlappingStrings(),
	))

	properties.Property("ReplaceAll returns error when pattern not found", prop.ForAll(
		func(pair stringPair) bool {
			source, pattern := pair.Source, pair.Pattern
			// Ensure pattern is not in source
			if strings.Contains(source, pattern) {
				return true // Skip this case - pattern exists
			}

			_, err := ReplaceAll(source, pattern, "replacement", "test.go", "test context")
			if err == nil {
				return false // Should have returned an error
			}

			var patchErr *PatchError
			if !errors.As(err, &patchErr) {
				return false // Should be a PatchError
			}

			return patchErr.File == "test.go" && patchErr.Context == "test context"
		},
		genNonOverlappingStrings(),
	))

	properties.Property("ReplaceOnce succeeds when pattern exists", prop.ForAll(
		func(source, pattern, replacement string) bool {
			// Ensure pattern is in source
			sourceWithPattern := source + pattern + source

			result, err := ReplaceOnce(sourceWithPattern, pattern, replacement, "test.go", "test context")
			if err != nil {
				return false // Should not have returned an error
			}

			// Verify replacement was made
			return strings.Contains(result, replacement)
		},
		gen.AlphaString(),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString(),
	))

	properties.Property("ReplaceAll succeeds when pattern exists", prop.ForAll(
		func(source, pattern, replacement string) bool {
			// Ensure pattern is in source multiple times
			sourceWithPattern := source + pattern + source + pattern + source
			originalPatternCount := strings.Count(sourceWithPattern, pattern)

			result, err := ReplaceAll(sourceWithPattern, pattern, replacement, "test.go", "test context")
			if err != nil {
				return false // Should not have returned an error
			}

			// The key property: if pattern != replacement and replacement doesn't contain pattern,
			// then pattern should not appear in result
			if pattern != replacement && !strings.Contains(replacement, pattern) {
				return !strings.Contains(result, pattern)
			}

			// Otherwise, just verify the replacement was applied by checking the result differs
			// from original (unless pattern == replacement)
			if pattern == replacement {
				return result == sourceWithPattern
			}

			// If replacement contains pattern, we can't easily verify, just check no error
			_ = originalPatternCount
			return true
		},
		gen.AlphaString(),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
	))

	properties.TestingRun(t)
}

// TestValidatePatternExistsProperty verifies pattern validation behavior.
func TestValidatePatternExistsProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("ValidatePatternExists returns error when pattern not found", prop.ForAll(
		func(pair stringPair) bool {
			source, pattern := pair.Source, pair.Pattern
			if strings.Contains(source, pattern) {
				return true // Skip - pattern exists
			}

			err := ValidatePatternExists(source, pattern, "test.go", "validation context")
			if err == nil {
				return false
			}

			var patchErr *PatchError
			return errors.As(err, &patchErr)
		},
		genNonOverlappingStrings(),
	))

	properties.Property("ValidatePatternExists returns nil when pattern exists", prop.ForAll(
		func(source, pattern string) bool {
			sourceWithPattern := source + pattern + source

			err := ValidatePatternExists(sourceWithPattern, pattern, "test.go", "validation context")
			return err == nil
		},
		gen.AlphaString(),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
	))

	properties.TestingRun(t)
}

// TestApplyPatchesProperty verifies batch patch application behavior.
func TestApplyPatchesProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("ApplyPatches reports errors for missing required patterns", prop.ForAll(
		func(source string, patches []Patch) bool {
			result, errs := ApplyPatches(source, "test.go", patches)

			// Count expected errors (non-optional patches with missing patterns)
			expectedErrors := 0
			for _, p := range patches {
				if !p.Optional && !strings.Contains(source, p.Old) {
					expectedErrors++
				}
			}

			// Verify error count matches
			if len(errs) != expectedErrors {
				return false
			}

			// Verify all errors are PatchErrors
			for _, err := range errs {
				var patchErr *PatchError
				if !errors.As(err, &patchErr) {
					return false
				}
			}

			// Result should still be returned (may be empty if source was empty)
			_ = result
			return true
		},
		gen.AlphaString(),
		genPatchSlice(),
	))

	properties.Property("ApplyPatches applies optional patches silently", prop.ForAll(
		func(source string) bool {
			patches := []Patch{
				{Old: "NONEXISTENT_PATTERN_12345", New: "replacement", Context: "optional patch", Optional: true},
			}

			_, errs := ApplyPatches(source, "test.go", patches)

			// Optional patches should not produce errors
			return len(errs) == 0
		},
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// TestPatchErrorMessageProperty verifies error messages are informative.
func TestPatchErrorMessageProperty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("PatchError message contains file and pattern info", prop.ForAll(
		func(file, pattern, context string) bool {
			// The PatchError stores the truncated pattern, so we need to truncate first
			truncatedPattern := truncatePattern(pattern)
			err := &PatchError{
				File:    file,
				Pattern: truncatedPattern,
				Context: context,
			}

			msg := err.Error()

			// Message should contain file name
			if !strings.Contains(msg, file) {
				return false
			}

			// Message should contain the truncated pattern
			if !strings.Contains(msg, truncatedPattern) {
				return false
			}

			// If context is provided, message should contain it
			if context != "" && !strings.Contains(msg, context) {
				return false
			}

			return true
		},
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// stringPair holds two strings for property testing.
type stringPair struct {
	Source  string
	Pattern string
}

// genNonOverlappingStrings generates two strings where the second is not a substring of the first.
func genNonOverlappingStrings() gopter.Gen {
	return gen.Struct(reflect.TypeOf(stringPair{}), map[string]gopter.Gen{
		"Source":  gen.AlphaString(),
		"Pattern": gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 5 }),
	}).SuchThat(func(v any) bool {
		s := v.(stringPair)
		return !strings.Contains(s.Source, s.Pattern)
	})
}

// genPatchSlice generates a slice of patches for testing.
func genPatchSlice() gopter.Gen {
	return gen.SliceOfN(3, genPatch())
}

// genPatch generates a single Patch for testing.
func genPatch() gopter.Gen {
	return gen.Struct(reflect.TypeOf(Patch{}), map[string]gopter.Gen{
		"Old":      gen.AlphaString().SuchThat(func(s string) bool { return len(s) > 0 }),
		"New":      gen.AlphaString(),
		"Context":  gen.AlphaString(),
		"Optional": gen.Bool(),
	})
}
