package search

import (
	"context"

	mongodriver "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type (
	sessionCollection interface {
		Find(ctx context.Context, filter any, opts ...*options.FindOptions) (cursor, error)
	}

	eventCollection interface {
		Find(ctx context.Context, filter any, opts ...*options.FindOptions) (cursor, error)
	}

	cursor interface {
		Next(ctx context.Context) bool
		Decode(val any) error
		Close(ctx context.Context) error
	}
)

type mongoCollection struct {
	coll *mongodriver.Collection
}

func (c mongoCollection) Find(ctx context.Context, filter any, opts ...*options.FindOptions) (cursor, error) {
	cur, err := c.coll.Find(ctx, filter, opts...)
	if err != nil {
		return nil, err
	}
	return mongoCursor{cursor: cur}, nil
}

type mongoCursor struct {
	cursor *mongodriver.Cursor
}

func (c mongoCursor) Next(ctx context.Context) bool { return c.cursor.Next(ctx) }
func (c mongoCursor) Decode(val any) error          { return c.cursor.Decode(val) }
func (c mongoCursor) Close(ctx context.Context) error {
	return c.cursor.Close(ctx)
}

// WrapSessions returns a sessionCollection backed by a Mongo collection.
func WrapSessions(coll *mongodriver.Collection) sessionCollection {
	return mongoCollection{coll: coll}
}

// WrapEvents returns an eventCollection backed by a Mongo collection.
func WrapEvents(coll *mongodriver.Collection) eventCollection {
	return mongoCollection{coll: coll}
}
