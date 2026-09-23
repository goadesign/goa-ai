// Consumer schemas remain available from the service design for existing users.
// Their definitions live in types so other designs can import them without
// registering the registry service.
package design

import registrytypes "goa.design/goa-ai/registry/design/types"

var (
	// ConsumerContract reuses the registry ConsumerContract schema.
	ConsumerContract = registrytypes.ConsumerContract
	// ToolSearchDocument reuses the registry ToolSearchDocument schema.
	ToolSearchDocument = registrytypes.ToolSearchDocument
	// ToolTypeMetadata reuses the registry ToolTypeMetadata schema.
	ToolTypeMetadata = registrytypes.ToolTypeMetadata
	// ToolFieldMetadata reuses the registry ToolFieldMetadata schema.
	ToolFieldMetadata = registrytypes.ToolFieldMetadata
	// ToolFieldPathSegment reuses the registry ToolFieldPathSegment schema.
	ToolFieldPathSegment = registrytypes.ToolFieldPathSegment
	// ToolCollectionElement reuses the registry ToolCollectionElement schema.
	ToolCollectionElement = registrytypes.ToolCollectionElement
	// ToolUnionBranch reuses the registry ToolUnionBranch schema.
	ToolUnionBranch = registrytypes.ToolUnionBranch
	// ToolBounds reuses the registry ToolBounds schema.
	ToolBounds = registrytypes.ToolBounds
	// ToolPaging reuses the registry ToolPaging schema.
	ToolPaging = registrytypes.ToolPaging
	// ToolConfirmation reuses the registry ToolConfirmation schema.
	ToolConfirmation = registrytypes.ToolConfirmation
	// ToolServerData reuses the registry ToolServerData schema.
	ToolServerData = registrytypes.ToolServerData
	// AgentToolTarget reuses the registry AgentToolTarget schema.
	AgentToolTarget = registrytypes.AgentToolTarget
)
