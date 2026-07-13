package bedrock

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestEncodeMessages_DocumentPartWithCitations(t *testing.T) {
	msgs := []*model.Message{
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.DocumentPart{
					Name:   "spec",
					Format: model.DocumentFormatTXT,
					Chunks: []string{"a", "b"},
					Cite:   true,
				},
			},
		},
	}
	got, _, err := encodeMessages(msgs, nil, false)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, brtypes.ConversationRoleUser, got[0].Role)
	require.Len(t, got[0].Content, 1)

	doc, ok := got[0].Content[0].(*brtypes.ContentBlockMemberDocument)
	require.True(t, ok)
	require.NotNil(t, doc.Value.Name)
	require.Equal(t, "spec", *doc.Value.Name)

	require.NotNil(t, doc.Value.Citations)
	require.NotNil(t, doc.Value.Citations.Enabled)
	require.True(t, *doc.Value.Citations.Enabled)

	source, ok := doc.Value.Source.(*brtypes.DocumentSourceMemberContent)
	require.True(t, ok)
	require.Len(t, source.Value, 2)
	_, ok = source.Value[0].(*brtypes.DocumentContentBlockMemberText)
	require.True(t, ok)
}

func TestEncodeMessages_DocumentPartS3SourceRejectsCitations(t *testing.T) {
	msgs := []*model.Message{
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.DocumentPart{
					Name:   "paper",
					Format: model.DocumentFormatPDF,
					URI:    "s3://bucket/key.pdf",
					Cite:   true,
				},
			},
		},
	}
	_, _, err := encodeMessages(msgs, nil, false)
	require.EqualError(t, err, `bedrock: document "paper" cannot enable citations for an S3 source`)
}

func TestTranslateResponse_CitationsContentBlock(t *testing.T) {
	out := &bedrockruntime.ConverseOutput{
		StopReason: brtypes.StopReasonEndTurn,
		Output: &brtypes.ConverseOutputMemberMessage{
			Value: brtypes.Message{
				Role: brtypes.ConversationRoleAssistant,
				Content: []brtypes.ContentBlock{
					&brtypes.ContentBlockMemberCitationsContent{
						Value: brtypes.CitationsContentBlock{
							Content: []brtypes.CitationGeneratedContent{
								&brtypes.CitationGeneratedContentMemberText{Value: "hello"},
							},
							Citations: []brtypes.Citation{
								{
									Title: aws.String("spec"),
									Location: &brtypes.CitationLocationMemberDocumentPage{
										Value: brtypes.DocumentPageLocation{
											DocumentIndex: aws.Int32(0),
											Start:         aws.Int32(1),
											End:           aws.Int32(1),
										},
									},
									SourceContent: []brtypes.CitationSourceContent{
										&brtypes.CitationSourceContentMemberText{Value: "cited"},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	resp, err := translateResponse(out, nil, "", "")
	require.NoError(t, err)
	require.Len(t, resp.Content, 1)
	require.Len(t, resp.Content[0].Parts, 1)

	part, ok := resp.Content[0].Parts[0].(model.CitationsPart)
	require.True(t, ok)
	require.Equal(t, "hello", part.Text)
	require.Len(t, part.Citations, 1)
	require.Equal(t, "spec", part.Citations[0].Title)
	require.NotNil(t, part.Citations[0].Location.DocumentPage)
	require.Equal(t, 1, part.Citations[0].Location.DocumentPage.Start)
	require.Equal(t, 1, part.Citations[0].Location.DocumentPage.End)
	require.Equal(t, "cited", part.Citations[0].SourceContent[0])
}

func TestTranslateResponse_CitationsReplayIntoNextTurnWithoutLoss(t *testing.T) {
	original := brtypes.CitationsContentBlock{
		Content: []brtypes.CitationGeneratedContent{
			&brtypes.CitationGeneratedContentMemberText{Value: "The cited answer."},
		},
		Citations: []brtypes.Citation{
			{
				Title:  aws.String("characters"),
				Source: aws.String("document-1"),
				Location: &brtypes.CitationLocationMemberDocumentChar{
					Value: brtypes.DocumentCharLocation{
						DocumentIndex: aws.Int32(0),
						Start:         aws.Int32(10),
						End:           aws.Int32(20),
					},
				},
				SourceContent: []brtypes.CitationSourceContent{
					&brtypes.CitationSourceContentMemberText{Value: "character excerpt"},
				},
			},
			{
				Title:  aws.String("chunks"),
				Source: aws.String("document-2"),
				Location: &brtypes.CitationLocationMemberDocumentChunk{
					Value: brtypes.DocumentChunkLocation{
						DocumentIndex: aws.Int32(1),
						Start:         aws.Int32(2),
						End:           aws.Int32(4),
					},
				},
				SourceContent: []brtypes.CitationSourceContent{
					&brtypes.CitationSourceContentMemberText{Value: "first chunk"},
					&brtypes.CitationSourceContentMemberText{Value: "second chunk"},
				},
			},
			{
				Title:  aws.String("pages"),
				Source: aws.String("document-3"),
				Location: &brtypes.CitationLocationMemberDocumentPage{
					Value: brtypes.DocumentPageLocation{
						DocumentIndex: aws.Int32(2),
						Start:         aws.Int32(7),
						End:           aws.Int32(8),
					},
				},
				SourceContent: []brtypes.CitationSourceContent{
					&brtypes.CitationSourceContentMemberText{Value: "page excerpt"},
				},
			},
		},
	}
	response, err := translateResponse(&bedrockruntime.ConverseOutput{
		StopReason: brtypes.StopReasonEndTurn,
		Output: &brtypes.ConverseOutputMemberMessage{
			Value: brtypes.Message{
				Role: brtypes.ConversationRoleAssistant,
				Content: []brtypes.ContentBlock{
					&brtypes.ContentBlockMemberCitationsContent{Value: original},
				},
			},
		},
	}, nil, "", "")
	require.NoError(t, err)

	conversation, _, err := encodeMessages([]*model.Message{
		{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Answer from the documents."}},
		},
		&response.Content[0],
		{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Now expand on that answer."}},
		},
	}, nil, false)
	require.NoError(t, err)
	require.Len(t, conversation, 3)
	require.Equal(t, brtypes.ConversationRoleAssistant, conversation[1].Role)

	replayed, ok := conversation[1].Content[0].(*brtypes.ContentBlockMemberCitationsContent)
	require.True(t, ok)
	require.Equal(t, original, replayed.Value)
}

func TestEncodeMessages_CitationsRequireAssistantHistory(t *testing.T) {
	citations := model.CitationsPart{
		Text: "answer",
		Citations: []model.Citation{{
			Title: "source",
			Location: model.CitationLocation{
				DocumentPage: &model.DocumentPageLocation{DocumentIndex: 0, Start: 1, End: 1},
			},
		}},
	}

	_, _, err := encodeMessages([]*model.Message{{
		Role:  model.ConversationRoleSystem,
		Parts: []model.Part{citations},
	}}, nil, false)
	require.EqualError(t, err, "bedrock: replaying canonical citations is not supported")

	_, _, err = encodeMessages([]*model.Message{{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{citations},
	}}, nil, false)
	require.EqualError(t, err, "bedrock: citation parts are only supported in assistant messages (role=user)")
}

func TestEncodeMessages_ValidatesCanonicalCitations(t *testing.T) {
	tests := []struct {
		name    string
		part    model.CitationsPart
		wantErr string
	}{
		{
			name:    "missing citations",
			part:    model.CitationsPart{Text: "answer"},
			wantErr: "bedrock: invalid citations part: citation list is empty",
		},
		{
			name: "multiple locations",
			part: model.CitationsPart{
				Text: "answer",
				Citations: []model.Citation{{
					Location: model.CitationLocation{
						DocumentChar: &model.DocumentCharLocation{},
						DocumentPage: &model.DocumentPageLocation{},
					},
				}},
			},
			wantErr: "bedrock: invalid citations part: citation 0 has multiple locations",
		},
		{
			name: "missing Bedrock location",
			part: model.CitationsPart{
				Text:      "answer",
				Citations: []model.Citation{{Title: "source"}},
			},
			wantErr: "bedrock: citation 0: requires exactly one Bedrock document location",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := encodeMessages([]*model.Message{{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{test.part},
			}}, nil, false)
			require.EqualError(t, err, test.wantErr)
		})
	}
}

func TestTranslateResponse_PreservesSingleAssistantMessageAcrossBlocks(t *testing.T) {
	out := &bedrockruntime.ConverseOutput{
		StopReason: brtypes.StopReasonEndTurn,
		Output: &brtypes.ConverseOutputMemberMessage{
			Value: brtypes.Message{
				Role: brtypes.ConversationRoleAssistant,
				Content: []brtypes.ContentBlock{
					&brtypes.ContentBlockMemberText{Value: `{"assistant_`},
					&brtypes.ContentBlockMemberText{Value: `text":"created a draft"}`},
				},
			},
		},
	}

	resp, err := translateResponse(out, nil, "", "")
	require.NoError(t, err)
	require.Len(t, resp.Content, 1)
	require.Equal(t, model.ConversationRoleAssistant, resp.Content[0].Role)
	require.Len(t, resp.Content[0].Parts, 2)

	first, ok := resp.Content[0].Parts[0].(model.TextPart)
	require.True(t, ok)
	require.Equal(t, `{"assistant_`, first.Text)
	second, ok := resp.Content[0].Parts[1].(model.TextPart)
	require.True(t, ok)
	require.Equal(t, `text":"created a draft"}`, second.Text)
}
