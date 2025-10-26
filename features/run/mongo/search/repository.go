package search

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"

	"goa.design/goa-ai/agents/runtime/run"
)

// SessionSortField enumerates supported sort fields.
type SessionSortField string

const (
	// SortByCreatedAt orders sessions by creation timestamp.
	SortByCreatedAt SessionSortField = "created_at"
	// SortByLastEvent orders sessions by last event timestamp.
	SortByLastEvent SessionSortField = "last_event_at"

	defaultSessionLimit = 50
	defaultFailureLimit = 50
)

// SessionCursor encodes pagination state for session searches.
type SessionCursor struct {
	Timestamp time.Time
	ID        primitive.ObjectID
}

// SessionSearchQuery captures filters for session lookups.
type SessionSearchQuery struct {
	OrgIDs         []string
	AgentIDs       []string
	PrincipalIDs   []string
	CreatedFrom    *time.Time
	CreatedTo      *time.Time
	LastEventFrom  *time.Time
	LastEventTo    *time.Time
	IncludeDeleted bool
	SortField      SessionSortField
	Descending     bool
	Limit          int
	Cursor         *SessionCursor
}

// SessionRecord represents a stored session.
type SessionRecord struct {
	RunID       string
	SessionID   string
	OrgID       string
	AgentID     string
	PrincipalID string
	Status      run.Status
	CreatedAt   time.Time
	UpdatedAt   time.Time
	LastEventAt *time.Time
	Labels      map[string]string
	DocumentID  primitive.ObjectID
}

// SessionSearchResult wraps the result set and next cursor.
type SessionSearchResult struct {
	Sessions   []SessionRecord
	NextCursor *SessionCursor
}

// FailureCursor encodes pagination state for failure logs.
type FailureCursor struct {
	Timestamp time.Time
	ID        primitive.ObjectID
}

// FailureQuery captures filters for tool failure searches.
type FailureQuery struct {
	OrgIDs      []string
	AgentIDs    []string
	ToolNames   []string
	ResultCodes []string
	From        *time.Time
	To          *time.Time
	Limit       int
	Cursor      *FailureCursor
}

// FailureRecord summarizes a failed tool invocation.
type FailureRecord struct {
	EventID    primitive.ObjectID
	RunID      string
	OrgID      string
	AgentID    string
	ToolName   string
	ResultCode string
	OccurredAt time.Time
	Payload    any
}

// SearchRepository exposes session and failure searches backed by Mongo.
type SearchRepository struct {
	sessions sessionCollection
	events   eventCollection
	timeout  time.Duration
}

// SearchOptions configures SearchRepository.
type SearchOptions struct {
	Sessions sessionCollection
	Events   eventCollection
	Timeout  time.Duration
}

// NewSearchRepository constructs a repository using the provided collections.
func NewSearchRepository(opts SearchOptions) (*SearchRepository, error) {
	if opts.Sessions == nil {
		return nil, errors.New("sessions collection is required")
	}
	if opts.Events == nil {
		return nil, errors.New("events collection is required")
	}
	return &SearchRepository{sessions: opts.Sessions, events: opts.Events, timeout: opts.Timeout}, nil
}

// Sessions returns session records honoring the provided query.
func (r *SearchRepository) Sessions(ctx context.Context, q SessionSearchQuery) (SessionSearchResult, error) {
	filter := buildSessionFilter(q)
	limit := int64(q.Limit)
	if limit <= 0 {
		limit = defaultSessionLimit
	}
	sortField := q.SortField
	if sortField == "" {
		sortField = SortByCreatedAt
	}
	order := 1
	if q.Descending {
		order = -1
	}
	opts := options.Find().SetSort(bson.D{{string(sortField), order}, {"_id", order}}).SetLimit(limit)
	ctx, cancel := r.withTimeout(ctx)
	defer cancel()
	cur, err := r.sessions.Find(ctx, filter, opts)
	if err != nil {
		return SessionSearchResult{}, err
	}
	defer cur.Close(ctx)

	var result SessionSearchResult
	for cur.Next(ctx) {
		var doc sessionDocument
		if err := cur.Decode(&doc); err != nil {
			return SessionSearchResult{}, err
		}
		result.Sessions = append(result.Sessions, doc.toRecord())
	}
	if len(result.Sessions) == int(limit) {
		last := result.Sessions[len(result.Sessions)-1]
		result.NextCursor = &SessionCursor{Timestamp: sortTimestamp(last, sortField), ID: last.DocumentID}
	}
	return result, nil
}

// Failures returns tool failure records honoring the provided query.
func (r *SearchRepository) Failures(ctx context.Context, q FailureQuery) ([]FailureRecord, *FailureCursor, error) {
	filter := buildFailureFilter(q)
	limit := int64(q.Limit)
	if limit <= 0 {
		limit = defaultFailureLimit
	}
	opts := options.Find().SetSort(bson.D{{"occurred_at", -1}, {"_id", -1}}).SetLimit(limit)
	ctx, cancel := r.withTimeout(ctx)
	defer cancel()
	cur, err := r.events.Find(ctx, filter, opts)
	if err != nil {
		return nil, nil, err
	}
	defer cur.Close(ctx)
	var records []FailureRecord
	for cur.Next(ctx) {
		var doc eventDocument
		if err := cur.Decode(&doc); err != nil {
			return nil, nil, err
		}
		records = append(records, doc.toFailure())
	}
	var next *FailureCursor
	if len(records) == int(limit) {
		last := records[len(records)-1]
		next = &FailureCursor{Timestamp: last.OccurredAt, ID: last.EventID}
	}
	return records, next, nil
}

func (r *SearchRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, r.timeout)
}

func buildSessionFilter(q SessionSearchQuery) bson.M {
	filter := bson.M{}
	addIn := func(field string, values []string) {
		if len(values) > 0 {
			filter[field] = bson.M{"$in": values}
		}
	}
	addIn("org_id", q.OrgIDs)
	addIn("agent_id", q.AgentIDs)
	addIn("principal.id", q.PrincipalIDs)
	addRange := func(field string, from, to *time.Time) {
		if from == nil && to == nil {
			return
		}
		rng := bson.M{}
		if from != nil {
			rng["$gte"] = *from
		}
		if to != nil {
			rng["$lte"] = *to
		}
		filter[field] = rng
	}
	addRange("created_at", q.CreatedFrom, q.CreatedTo)
	addRange("last_event_at", q.LastEventFrom, q.LastEventTo)
	if !q.IncludeDeleted {
		filter["deleted_at"] = bson.M{"$exists": false}
	}
	if cursor := q.Cursor; cursor != nil && cursor.ID != primitive.NilObjectID {
		field := string(q.SortField)
		if field == "" {
			field = string(SortByCreatedAt)
		}
		cmp := "$gt"
		if q.Descending {
			cmp = "$lt"
		}
		filter["$or"] = []bson.M{
			{field: bson.M{cmp: cursor.Timestamp}},
			{field: cursor.Timestamp, "_id": bson.M{cmp: cursor.ID}},
		}
	}
	return filter
}

func sortTimestamp(rec SessionRecord, sortField SessionSortField) time.Time {
	switch sortField {
	case SortByLastEvent:
		if rec.LastEventAt != nil {
			return *rec.LastEventAt
		}
	}
	return rec.CreatedAt
}

func buildFailureFilter(q FailureQuery) bson.M {
	filter := bson.M{"type": "tool_result"}
	addIn := func(field string, values []string) {
		if len(values) > 0 {
			filter[field] = bson.M{"$in": values}
		}
	}
	addIn("org_id", q.OrgIDs)
	addIn("agent_id", q.AgentIDs)
	addIn("tool_name", q.ToolNames)
	addIn("result_code", q.ResultCodes)
	if q.From != nil || q.To != nil {
		rng := bson.M{}
		if q.From != nil {
			rng["$gte"] = *q.From
		}
		if q.To != nil {
			rng["$lte"] = *q.To
		}
		filter["occurred_at"] = rng
	}
	if cursor := q.Cursor; cursor != nil && cursor.ID != primitive.NilObjectID {
		filter["$or"] = []bson.M{
			{"occurred_at": bson.M{"$lt": cursor.Timestamp}},
			{"occurred_at": cursor.Timestamp, "_id": bson.M{"$lt": cursor.ID}},
		}
	}
	return filter
}

type sessionDocument struct {
	ID          primitive.ObjectID `bson:"_id"`
	RunID       string             `bson:"run_id"`
	SessionID   string             `bson:"session_id"`
	OrgID       string             `bson:"org_id"`
	AgentID     string             `bson:"agent_id"`
	Principal   principalDoc       `bson:"principal"`
	Status      run.Status         `bson:"status"`
	CreatedAt   time.Time          `bson:"created_at"`
	UpdatedAt   time.Time          `bson:"updated_at"`
	LastEventAt *time.Time         `bson:"last_event_at"`
	Labels      map[string]string  `bson:"labels"`
}

type principalDoc struct {
	ID string `bson:"id"`
}

func (d sessionDocument) toRecord() SessionRecord {
	return SessionRecord{
		RunID:       d.RunID,
		SessionID:   d.SessionID,
		OrgID:       d.OrgID,
		AgentID:     d.AgentID,
		PrincipalID: d.Principal.ID,
		Status:      d.Status,
		CreatedAt:   d.CreatedAt,
		UpdatedAt:   d.UpdatedAt,
		LastEventAt: d.LastEventAt,
		Labels:      d.Labels,
		DocumentID:  d.ID,
	}
}

type eventDocument struct {
	ID         primitive.ObjectID `bson:"_id"`
	RunID      string             `bson:"run_id"`
	OrgID      string             `bson:"org_id"`
	AgentID    string             `bson:"agent_id"`
	ToolName   string             `bson:"tool_name"`
	ResultCode string             `bson:"result_code"`
	OccurredAt time.Time          `bson:"occurred_at"`
	Payload    any                `bson:"payload"`
}

func (d eventDocument) toFailure() FailureRecord {
	return FailureRecord{
		EventID:    d.ID,
		RunID:      d.RunID,
		OrgID:      d.OrgID,
		AgentID:    d.AgentID,
		ToolName:   d.ToolName,
		ResultCode: d.ResultCode,
		OccurredAt: d.OccurredAt,
		Payload:    d.Payload,
	}
}
