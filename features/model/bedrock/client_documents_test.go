package bedrock

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestEncodeMessages_DocumentPartWithCitations(t *testing.T) {
	ctx := context.Background()

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
	got, _, err := encodeMessages(ctx, msgs, nil, false, nil)
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

func TestEncodeMessages_DocumentPartS3Source(t *testing.T) {
	ctx := context.Background()

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
	got, _, err := encodeMessages(ctx, msgs, nil, false, nil)
	require.NoError(t, err)
	require.Len(t, got, 1)

	doc, ok := got[0].Content[0].(*brtypes.ContentBlockMemberDocument)
	require.True(t, ok)
	require.Nil(t, doc.Value.Citations)
	source, ok := doc.Value.Source.(*brtypes.DocumentSourceMemberS3Location)
	require.True(t, ok)
	require.NotNil(t, source.Value.Uri)
	require.Equal(t, "s3://bucket/key.pdf", *source.Value.Uri)
}

func TestTranslateResponse_CitationsContentBlock(t *testing.T) {
	out := &bedrockruntime.ConverseOutput{
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

	resp, err := translateResponse(out, nil)
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
