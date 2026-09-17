// Package openai ranks the current request's deferred tools with BM25, a lexical
// search score based on word frequency and document length. Names, titles, and
// descriptions stay in this private index, outside the model's initial context.
package openai

import (
	"cmp"
	"math"
	"slices"

	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	searchDocument struct {
		name     string
		document tools.SearchDocument
	}

	searchPosting struct {
		document  int
		frequency int
	}

	searchIndex struct {
		names         []string
		postings      map[string][]searchPosting
		lengths       []int
		averageLength float64
	}

	searchHit struct {
		name  string
		score float64
	}
)

// newSearchIndex indexes only the permitted deferred tools supplied by request
// preparation. The immutable index is reused across one invocation's searches.
func newSearchIndex(documents []searchDocument) *searchIndex {
	index := &searchIndex{
		names:    make([]string, len(documents)),
		postings: make(map[string][]searchPosting),
		lengths:  make([]int, len(documents)),
	}
	totalLength := 0.0
	for i, document := range documents {
		index.names[i] = document.name
		index.lengths[i] = document.document.Length
		totalLength += float64(document.document.Length)
		for term, frequency := range document.document.Terms {
			index.postings[term] = append(index.postings[term], searchPosting{
				document:  i,
				frequency: frequency,
			})
		}
	}
	if len(documents) > 0 {
		index.averageLength = totalLength / float64(len(documents))
	}
	return index
}

// search returns at most limit positive-scoring candidates. Equal scores sort
// by canonical identity. This ranks relevance; it never grants permission.
func (index *searchIndex) search(query string, limit int) []searchHit {
	const k1, b = 1.2, 0.75
	document := tools.NewSearchDocument(query)
	terms := make([]string, 0, len(document.Terms))
	for term := range document.Terms {
		terms = append(terms, term)
	}
	slices.Sort(terms)
	scores := make([]float64, len(index.names))
	for _, term := range terms {
		postings := index.postings[term]
		idf := math.Log1p((float64(len(index.names)-len(postings)) + 0.5) / (float64(len(postings)) + 0.5))
		for _, posting := range postings {
			frequency := float64(posting.frequency)
			lengthRatio := float64(index.lengths[posting.document]) / index.averageLength
			denominator := frequency + k1*(1-b+b*lengthRatio)
			scores[posting.document] += idf * frequency * (k1 + 1) / denominator
		}
	}
	hits := make([]searchHit, 0, len(scores))
	for i, score := range scores {
		if score > 0 {
			hits = append(hits, searchHit{name: index.names[i], score: score})
		}
	}
	slices.SortFunc(hits, func(a, b searchHit) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(a.name, b.name))
	})
	return hits[:min(limit, len(hits))]
}
