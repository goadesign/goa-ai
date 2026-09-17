// Package tools stores search words prepared from tool descriptions. Generated
// tools contain literal counts; providers with dynamic descriptions prepare
// these counts once when registering a new definition.
package tools

import (
	"errors"
	"strings"
	"unicode"
)

type (
	// SearchDocument contains the word counts used to rank a tool for a query.
	// It contains no permissions, schemas, or execution routing.
	SearchDocument struct {
		// Length is the number of words, including repeated words.
		Length int
		// Terms maps each lowercase letter/digit word to its positive count.
		Terms map[string]int
	}
)

// NewSearchDocument counts lowercase words separated by punctuation or spaces.
// Code generation calls it with a tool's name, title, and description, then
// emits the result as literals. Dynamic catalog owners call it when a tool's
// text changes. Search engines also use it to prepare incoming query text.
func NewSearchDocument(text string) SearchDocument {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	document := SearchDocument{Length: len(words), Terms: make(map[string]int)}
	for _, word := range words {
		document.Terms[word]++
	}
	return document
}

// Validate checks counts received at a model or registry boundary. The empty
// document is valid for a tool that does not support lexical discovery.
func (d SearchDocument) Validate() error {
	length := 0
	for term, count := range d.Terms {
		if term == "" || count <= 0 || length+count < length {
			return errors.New("tool search document has an invalid word count")
		}
		length += count
	}
	if d.Length != length {
		return errors.New("tool search document length does not match its word counts")
	}
	return nil
}
