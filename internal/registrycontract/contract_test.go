package registrycontract

import genregistry "goa.design/goa-ai/registry/gen/registry"

func testDeclaration() *genregistry.ToolSchema {
	document := []byte(`{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"],"additionalProperties":false}`)
	reminder, continuation := "Report the returned value.", "records.continue"
	return &genregistry.ToolSchema{
		Name:                   "records.change",
		PayloadSchema:          document,
		ExecutionPayloadSchema: document,
		ResultSchema:           document,
		ConsumerContract: &genregistry.ConsumerContract{
			Kind: "service", Title: "Change record",
			Search: &genregistry.ToolSearchDocument{Length: 1, Terms: map[string]int{"records": 1}},
			Meta:   map[string][]string{"scope": {"facility"}},
			Payload: &genregistry.ToolTypeMetadata{
				SchemaWithoutRootExample: document,
				Fields: []*genregistry.ToolFieldMetadata{{
					Path: []*genregistry.ToolFieldPathSegment{
						{Segment: genregistry.NewToolFieldSegmentField("items")},
						{Segment: genregistry.NewToolFieldSegmentElement(&genregistry.ToolCollectionElement{})},
						{Segment: genregistry.NewToolFieldSegmentField("*")},
					},
					Branches: []*genregistry.ToolUnionBranch{{
						Discriminator: []*genregistry.ToolFieldPathSegment{
							{Segment: genregistry.NewToolFieldSegmentField("choice")},
							{Segment: genregistry.NewToolFieldSegmentField("type")},
						},
						Value: "text",
					}},
				}},
			},
			Result: &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: document},
			Bounds: &genregistry.ToolBounds{Paging: &genregistry.ToolPaging{
				ContinueTool: &continuation, CursorField: "cursor", NextCursorField: "next_cursor",
			}},
			Confirmation: &genregistry.ToolConfirmation{
				PromptTemplate:       "Confirm {{ json .value }}",
				DeniedResultTemplate: `{"value":0}`,
			},
			ServerData: []*genregistry.ToolServerData{{
				Kind: "record", Audience: "timeline", Schema: document,
				Type: &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: document},
			}},
			ResultReminder: &reminder,
		},
	}
}
