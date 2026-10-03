package assistantapi

import (
	"context"

	genassistant "example.com/assistant/gen/assistant"
	"goa.design/clue/log"
)

// assistant service example implementation.
// The methods return stable values so protocol tests prove the generated
// codecs preserve complete results.
type assistantsrvc struct{}

// NewAssistant returns the assistant service implementation.
func NewAssistant() genassistant.Service {
	return &assistantsrvc{}
}

// List available documents
func (s *assistantsrvc) ListDocuments(ctx context.Context) (res *genassistant.Documents, err error) {
	res = &genassistant.Documents{Items: []string{"guide.md", "reference.md"}}
	log.Printf(ctx, "assistant.list_documents")
	return
}

// Return system info
func (s *assistantsrvc) SystemInfo(ctx context.Context) (res *genassistant.SystemInfoResult, err error) {
	name, version := "assistant", "1.0.0"
	res = &genassistant.SystemInfoResult{Name: &name, Version: &version}
	log.Printf(ctx, "assistant.system_info")
	return
}

// Analyze sentiment of text
func (s *assistantsrvc) AnalyzeSentiment(ctx context.Context, p *genassistant.AnalyzeSentimentPayload) (res *genassistant.AnalyzeSentimentResult, err error) {
	sentiment := "positive"
	res = &genassistant.AnalyzeSentimentResult{Sentiment: &sentiment}
	log.Printf(ctx, "assistant.analyze_sentiment")
	return
}

// Extract keywords from text
func (s *assistantsrvc) ExtractKeywords(ctx context.Context, p *genassistant.ExtractKeywordsPayload) (res *genassistant.ExtractKeywordsResult, err error) {
	res = &genassistant.ExtractKeywordsResult{Keywords: []string{"MCP", "typed", "tools"}}
	log.Printf(ctx, "assistant.extract_keywords")
	return
}

// Summarize text
func (s *assistantsrvc) SummarizeText(ctx context.Context, p *genassistant.SummarizeTextPayload) (res *genassistant.SummarizeTextResult, err error) {
	summary := "The fixture returns a stable generated response."
	res = &genassistant.SummarizeTextResult{Summary: &summary}
	log.Printf(ctx, "assistant.summarize_text")
	return
}

// Search knowledge base
func (s *assistantsrvc) Search(ctx context.Context, p *genassistant.SearchPayload) (res *genassistant.SearchResult, err error) {
	res = &genassistant.SearchResult{Results: []string{"MCP protocol reference", "Generated tool guide"}}
	log.Printf(ctx, "assistant.search")
	return
}

// Execute code
func (s *assistantsrvc) ExecuteCode(ctx context.Context, p *genassistant.ExecuteCodePayload) (res *genassistant.ExecuteCodeResult, err error) {
	output := "4"
	res = &genassistant.ExecuteCodeResult{Output: &output}
	log.Printf(ctx, "assistant.execute_code")
	return
}

// Process batch of items
func (s *assistantsrvc) ProcessBatch(ctx context.Context, p *genassistant.ProcessBatchPayload) (res *genassistant.ProcessBatchResult, err error) {
	ok := true
	res = &genassistant.ProcessBatchResult{OK: &ok}
	log.Printf(ctx, "assistant.process_batch")
	return
}

// BinaryResource returns synthetic bytes so independent clients can verify that
// the generated resource adapter preserves binary data without JSON quoting.
func (s *assistantsrvc) BinaryResource(_ context.Context) (genassistant.Image, error) {
	return genassistant.Image{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, 0x00, 0x00, 0x00,
		0x0c, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x60, 0x60, 0x60, 0x00,
		0x00, 0x00, 0x04, 0x00, 0x01, 0xf6, 0x17, 0x38, 0x55, 0x00, 0x00, 0x00,
		0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}, nil
}

// EmptyBinaryResource returns an existing resource with no bytes, which clients
// receive as a present empty blob rather than missing content.
func (s *assistantsrvc) EmptyBinaryResource(_ context.Context) ([]byte, error) {
	return []byte{}, nil
}
