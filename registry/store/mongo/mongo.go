// Package mongo provides a MongoDB implementation of the registry store.
//
// This implementation persists toolset metadata to MongoDB for durability
// across restarts, suitable for production deployments.
package mongo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/registry/store"
)

// Store is a MongoDB implementation of the store.Store interface.
// It persists toolset metadata to MongoDB for durability across restarts.
type Store struct {
	collection *mongo.Collection
}

// Compile-time check that Store implements store.Store.
var _ store.Store = (*Store)(nil)

// toolsetDocument is the MongoDB document representation of a Toolset.
type toolsetDocument struct {
	Name         string         `bson:"_id"`
	Description  *string        `bson:"description,omitempty"`
	Version      *string        `bson:"version,omitempty"`
	Tags         []string       `bson:"tags,omitempty"`
	Tools        []toolDocument `bson:"tools,omitempty"`
	StreamID     string         `bson:"stream_id"`
	RegisteredAt string         `bson:"registered_at"`
}

// toolDocument is the MongoDB document representation of a Tool.
type toolDocument struct {
	Name          string   `bson:"name"`
	Description   *string  `bson:"description,omitempty"`
	Tags          []string `bson:"tags,omitempty"`
	PayloadSchema []byte   `bson:"payload_schema"`
	ResultSchema  []byte   `bson:"result_schema"`
	SidecarSchema []byte   `bson:"sidecar_schema,omitempty"`
}

// New creates a new MongoDB store using the provided collection.
// The collection should be from a connected MongoDB client.
func New(collection *mongo.Collection) *Store {
	return &Store{
		collection: collection,
	}
}

// SaveToolset stores or updates a toolset in MongoDB.
func (s *Store) SaveToolset(ctx context.Context, toolset *genregistry.Toolset) error {
	doc := toDocument(toolset)
	opts := options.Replace().SetUpsert(true)
	_, err := s.collection.ReplaceOne(ctx, bson.M{"_id": toolset.Name}, doc, opts)
	if err != nil {
		return fmt.Errorf("mongodb save toolset %q: %w", toolset.Name, err)
	}
	return nil
}

// GetToolset retrieves a toolset by name from MongoDB.
func (s *Store) GetToolset(ctx context.Context, name string) (*genregistry.Toolset, error) {
	var doc toolsetDocument
	err := s.collection.FindOne(ctx, bson.M{"_id": name}).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("mongodb get toolset %q: %w", name, err)
	}
	return fromDocument(&doc), nil
}

// DeleteToolset removes a toolset by name from MongoDB.
func (s *Store) DeleteToolset(ctx context.Context, name string) error {
	result, err := s.collection.DeleteOne(ctx, bson.M{"_id": name})
	if err != nil {
		return fmt.Errorf("mongodb delete toolset %q: %w", name, err)
	}
	if result.DeletedCount == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ListToolsets returns all toolsets from MongoDB, optionally filtered by tags.
func (s *Store) ListToolsets(ctx context.Context, tags []string) ([]*genregistry.Toolset, error) {
	filter := bson.M{}
	if len(tags) > 0 {
		filter["tags"] = bson.M{"$all": tags}
	}

	cursor, err := s.collection.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("mongodb list toolsets: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var docs []toolsetDocument
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("mongodb list toolsets decode: %w", err)
	}

	result := make([]*genregistry.Toolset, len(docs))
	for i, doc := range docs {
		result[i] = fromDocument(&doc)
	}
	return result, nil
}

// SearchToolsets searches toolsets by query string in MongoDB.
// The query is matched against name, description, and tags (case-insensitive).
func (s *Store) SearchToolsets(ctx context.Context, query string) ([]*genregistry.Toolset, error) {
	escapedQuery := escapeRegex(query)
	regex := bson.M{"$regex": escapedQuery, "$options": "i"}
	filter := bson.M{
		"$or": []bson.M{
			{"_id": regex},
			{"description": regex},
			{"tags": regex},
		},
	}

	cursor, err := s.collection.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("mongodb search toolsets: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var docs []toolsetDocument
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("mongodb search toolsets decode: %w", err)
	}

	result := make([]*genregistry.Toolset, len(docs))
	for i, doc := range docs {
		result[i] = fromDocument(&doc)
	}
	return result, nil
}

// ListAll returns all toolsets from MongoDB without any filtering.
// This is used for restoring toolsets on registry restart.
func (s *Store) ListAll(ctx context.Context) ([]*genregistry.Toolset, error) {
	return s.ListToolsets(ctx, nil)
}

// toDocument converts a Toolset to a MongoDB document.
func toDocument(ts *genregistry.Toolset) *toolsetDocument {
	tools := make([]toolDocument, len(ts.Tools))
	for i, t := range ts.Tools {
		tools[i] = toolDocument{
			Name:          t.Name,
			Description:   t.Description,
			Tags:          t.Tags,
			PayloadSchema: t.PayloadSchema,
			ResultSchema:  t.ResultSchema,
			SidecarSchema: t.SidecarSchema,
		}
	}
	var version *string
	if ts.Version != nil {
		v := string(*ts.Version)
		version = &v
	}
	// Ensure tags is never nil for MongoDB $all queries
	tags := ts.Tags
	if tags == nil {
		tags = []string{}
	}
	return &toolsetDocument{
		Name:         ts.Name,
		Description:  ts.Description,
		Version:      version,
		Tags:         tags,
		Tools:        tools,
		StreamID:     ts.StreamID,
		RegisteredAt: ts.RegisteredAt,
	}
}

// fromDocument converts a MongoDB document to a Toolset.
func fromDocument(doc *toolsetDocument) *genregistry.Toolset {
	tools := make([]*genregistry.ToolSchema, len(doc.Tools))
	for i, t := range doc.Tools {
		tools[i] = &genregistry.ToolSchema{
			Name:          t.Name,
			Description:   t.Description,
			Tags:          t.Tags,
			PayloadSchema: t.PayloadSchema,
			ResultSchema:  t.ResultSchema,
			SidecarSchema: t.SidecarSchema,
		}
	}
	var version *genregistry.SemVer
	if doc.Version != nil {
		v := genregistry.SemVer(*doc.Version)
		version = &v
	}
	return &genregistry.Toolset{
		Name:         doc.Name,
		Description:  doc.Description,
		Version:      version,
		Tags:         doc.Tags,
		Tools:        tools,
		StreamID:     doc.StreamID,
		RegisteredAt: doc.RegisteredAt,
	}
}

// escapeRegex escapes special regex characters for safe use in MongoDB regex queries.
func escapeRegex(s string) string {
	special := []string{"\\", ".", "+", "*", "?", "^", "$", "(", ")", "[", "]", "{", "}", "|"}
	result := s
	for _, char := range special {
		result = strings.ReplaceAll(result, char, "\\"+char)
	}
	return result
}
