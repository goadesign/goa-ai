// Package shared provides common utilities for code generation across protocols.
//
// # Patch Utilities
//
// The patch utilities in this file provide validated string replacement for
// modifying Goa-generated code. Unlike raw strings.Replace, these functions
// validate that expected patterns exist before replacing, ensuring that
// template changes in Goa are detected early rather than silently producing
// incorrect code.
//
// ## Usage Patterns
//
// Required patches (must succeed):
//
//	patched, err := shared.ReplaceOnce(source, old, new, file, context)
//	if err != nil {
//	    log.Printf("WARNING: %v", err)
//	} else {
//	    s.Source = patched
//	}
//
// Optional patches (may not apply):
//
//	s.Source = shared.ReplaceOnceOptional(s.Source, old, new)
//
// Batch patches:
//
//	result, errs := shared.ApplyPatches(source, file, []Patch{
//	    {Old: "pattern1", New: "replacement1", Context: "description"},
//	    {Old: "pattern2", New: "replacement2", Optional: true},
//	})
//
// ## Error Handling
//
// PatchError provides structured error information including:
//   - File: The file being patched
//   - Pattern: The pattern that was not found (truncated for display)
//   - Context: Description of what the patch was trying to do
package shared

import (
	"fmt"
	"strings"
)

// PatchError represents an error that occurred during template patching.
type PatchError struct {
	// File is the path of the file being patched.
	File string
	// Pattern is the pattern that was expected but not found.
	Pattern string
	// Context provides additional context about the patch operation.
	Context string
}

func (e *PatchError) Error() string {
	if e.Context != "" {
		return fmt.Sprintf("patch failed for %s: pattern not found (%s): %q", e.File, e.Context, e.Pattern)
	}
	return fmt.Sprintf("patch failed for %s: pattern not found: %q", e.File, e.Pattern)
}

// PatchResult tracks the result of a patch operation.
type PatchResult struct {
	// Patched indicates whether the patch was applied.
	Patched bool
	// Source is the resulting source after patching.
	Source string
}

// ReplaceOnce replaces the first occurrence of old with new in source.
// Returns an error if old is not found in source.
func ReplaceOnce(source, old, new, file, context string) (string, error) {
	if !strings.Contains(source, old) {
		return source, &PatchError{File: file, Pattern: truncatePattern(old), Context: context}
	}
	return strings.Replace(source, old, new, 1), nil
}

// ReplaceAll replaces all occurrences of old with new in source.
// Returns an error if old is not found in source.
func ReplaceAll(source, old, new, file, context string) (string, error) {
	if !strings.Contains(source, old) {
		return source, &PatchError{File: file, Pattern: truncatePattern(old), Context: context}
	}
	return strings.ReplaceAll(source, old, new), nil
}

// ReplaceOnceOptional replaces the first occurrence of old with new in source.
// Does not return an error if old is not found (for optional patches).
func ReplaceOnceOptional(source, old, new string) string {
	return strings.Replace(source, old, new, 1)
}

// ReplaceAllOptional replaces all occurrences of old with new in source.
// Does not return an error if old is not found (for optional patches).
func ReplaceAllOptional(source, old, new string) string {
	return strings.ReplaceAll(source, old, new)
}

// truncatePattern truncates a pattern for display in error messages.
func truncatePattern(pattern string) string {
	const maxLen = 80
	if len(pattern) <= maxLen {
		return pattern
	}
	return pattern[:maxLen] + "..."
}

// ValidatePatternExists checks if a pattern exists in the source.
// Returns an error if the pattern is not found.
func ValidatePatternExists(source, pattern, file, context string) error {
	if !strings.Contains(source, pattern) {
		return &PatchError{File: file, Pattern: truncatePattern(pattern), Context: context}
	}
	return nil
}

// Patch represents a single string replacement operation.
// Each patch specifies an old pattern to find and a new string to replace it with.
type Patch struct {
	Old     string
	New     string
	Context string
	// Optional indicates this patch may not apply (pattern may not exist).
	Optional bool
}

// ApplyPatches applies a series of patches to a source string.
// Returns the patched source and any errors for non-optional patches that failed.
func ApplyPatches(source, file string, patches []Patch) (string, []error) { //nolint:unparam // source is used in tests
	var errs []error
	result := source
	for _, p := range patches {
		if p.Optional {
			result = strings.Replace(result, p.Old, p.New, 1)
		} else {
			patched, err := ReplaceOnce(result, p.Old, p.New, file, p.Context)
			if err != nil {
				errs = append(errs, err)
			} else {
				result = patched
			}
		}
	}
	return result, errs
}
