// Package mcp stores private OAuth credentials for one host user or application.
// The host supplies storage and serializes access to each framework-derived key.
// Generated codecs own record validation; no credential belongs in agent state.
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"slices"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

type (
	// AuthorizationStore owns private credential storage for one host user or
	// application. Different users must have separate storage namespaces, even
	// when they use the same registered client. A durable implementation must
	// encrypt records and serialize each key across all instances using it.
	AuthorizationStore interface {
		// WithCredentials gives use exclusive access to all requested records until use
		// returns. On success it calls use exactly once. Keys are distinct; records
		// follow their input order and are non-nil.
		// Waiting must respect ctx. Each Save commits independently before
		// returning; returning an error must not undo an earlier successful Save.
		// Keys are opaque framework values. Neither keys nor records belong in logs.
		WithCredentials(ctx context.Context, keys []string, use func([]AuthorizationCredential) error) error
	}
	// AuthorizationCredential reads and saves one record during WithCredentials.
	// Its methods use that operation's cancellation context. Records contain
	// private credentials and must be retained exactly as supplied by Goa-AI.
	AuthorizationCredential interface {
		// Load returns a copy of the saved record and explicit presence. An absent
		// record returns exists=false; a present malformed record is an error in
		// the generated decoder, not permission to start over silently.
		Load() (data []byte, exists bool, err error)
		// Save commits a copy of data before returning. An error may have an
		// uncertain commit outcome; the next Load must report the stored truth.
		Save(data []byte) error
	}
	// memoryAuthorizationStore retains explicit process-lifetime credentials.
	memoryAuthorizationStore struct {
		mutex   sync.Mutex
		records map[string]*memoryAuthorizationRecord
	}
	// memoryAuthorizationRecord serializes independent accesses to one key.
	memoryAuthorizationRecord struct {
		lock   chan struct{}
		data   []byte
		exists bool
	}
	// memoryAuthorizationCredential binds storage methods to the waiting caller.
	memoryAuthorizationCredential struct {
		ctx    context.Context
		record *memoryAuthorizationRecord
	}
)

// NewMemoryAuthorizationStore constructs storage for one user or application
// whose credentials need only survive this process. Share it across that user's
// transports; use an encrypted durable implementation when restarts must retain
// grants. Both implementations use the same acquisition and rotation path.
func NewMemoryAuthorizationStore() AuthorizationStore {
	return &memoryAuthorizationStore{records: make(map[string]*memoryAuthorizationRecord)}
}

// WithCredentials acquires keys in a fixed order, so concurrent requests for
// overlapping resource and identity records cannot wait on each other forever.
func (s *memoryAuthorizationStore) WithCredentials(ctx context.Context, keys []string, use func([]AuthorizationCredential) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mutex.Lock()
	selected := make(map[string]*memoryAuthorizationRecord, len(keys))
	for _, key := range keys {
		record, ok := s.records[key]
		if !ok {
			record = &memoryAuthorizationRecord{lock: make(chan struct{}, 1)}
			s.records[key] = record
		}
		selected[key] = record
	}
	s.mutex.Unlock()
	ordered := slices.Clone(keys)
	slices.Sort(ordered)
	acquired := make([]*memoryAuthorizationRecord, 0, len(ordered))
	defer func() {
		for _, record := range acquired {
			<-record.lock
		}
	}()
	for _, key := range ordered {
		record := selected[key]
		select {
		case record.lock <- struct{}{}:
			acquired = append(acquired, record)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	records := make([]AuthorizationCredential, len(keys))
	for n, key := range keys {
		records[n] = &memoryAuthorizationCredential{ctx: ctx, record: selected[key]}
	}
	return use(records)
}

// Load returns private copies so a caller cannot change a committed credential.
func (r *memoryAuthorizationCredential) Load() ([]byte, bool, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, false, err
	}
	return slices.Clone(r.record.data), r.record.exists, nil
}

// Save commits a private copy before another caller can acquire this record.
func (r *memoryAuthorizationCredential) Save(data []byte) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	r.record.data, r.record.exists = slices.Clone(data), true
	return nil
}

// withAuthorizationCredentials holds all records needed by one grant together.
// Host storage errors cannot expose credentials; safe grant errors remain intact.
func withAuthorizationCredentials(ctx context.Context, store AuthorizationStore, keys []string, use func(context.Context, []AuthorizationCredential) error) (err error) {
	ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.oauth.credentials")
	defer span.End()
	span.SetAttributes(attribute.Int("oauth.credential.records", len(keys)))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
	}()
	var operationErr error
	called := false
	err = store.WithCredentials(ctx, keys, func(records []AuthorizationCredential) error {
		if called {
			operationErr = errors.New("mcp: authorization store invoked the credential operation more than once")
			return operationErr
		}
		called = true
		if len(records) != len(keys) {
			operationErr = errors.New("mcp: authorization store returned an invalid record count")
			return operationErr
		}
		operationErr = use(ctx, records)
		return operationErr
	})
	if operationErr != nil {
		return operationErr
	}
	if err != nil {
		return authorizationFailure(ctx, "credential storage", err)
	}
	if !called {
		return errors.New("mcp: authorization store did not provide credential records")
	}
	return nil
}

// authorizationCredentialKey hashes length-prefixed identity fields, so exact
// issuer, client and resource tuples cannot collide by joining their strings.
func authorizationCredentialKey(binding []string) string {
	var data []byte
	for _, value := range binding {
		data = binary.BigEndian.AppendUint64(data, uint64(len(value)))
		data = append(data, value...)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
