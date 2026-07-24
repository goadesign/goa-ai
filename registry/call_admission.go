// Package registry owns cross-replica admission for routed tool calls.
//
// A Redis-expiring hash keyed by transport identity and admission token binds
// retries to one immutable request. A short exact-owner publication lock allows
// only one registry replica to publish or republish at a time. Pulse remains
// the immutable result history; this store owns only publication coordination.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type (
	// callAdmissionStore serializes publication attempts across registry nodes.
	callAdmissionStore struct {
		redis  *redis.Client
		prefix string
	}

	// callAdmission is the persisted publication state for one immutable call.
	callAdmission struct {
		key    string
		digest string
	}

	// callPublication proves temporary exact ownership of one publish attempt.
	callPublication struct {
		store *callAdmissionStore
		key   string
		owner string
	}
)

const callPublicationLockTTL = 10 * time.Second

var (
	errCallAdmissionConflict = errors.New("tool call admission conflicts with immutable request")

	ensureCallAdmissionScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 0 then
  redis.call("HSET", KEYS[1], "digest", ARGV[1], "published", "0", "overload", "")
  redis.call("PEXPIRE", KEYS[1], ARGV[2])
  return {1, ARGV[1], "0", ""}
end
local digest = redis.call("HGET", KEYS[1], "digest")
if digest ~= ARGV[1] then
  return {-1, digest or "", redis.call("HGET", KEYS[1], "published") or "0", redis.call("HGET", KEYS[1], "overload") or ""}
end
redis.call("PEXPIRE", KEYS[1], ARGV[2])
return {0, digest, redis.call("HGET", KEYS[1], "published") or "0", redis.call("HGET", KEYS[1], "overload") or ""}
`)
	markCallPublishedScript = redis.NewScript(`
if redis.call("HGET", KEYS[1], "digest") ~= ARGV[1] then
  return 0
end
redis.call("HSET", KEYS[1], "published", "1", "overload", ARGV[2])
redis.call("PEXPIRE", KEYS[1], ARGV[3])
return 1
`)
	releaseCallPublicationScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)
)

// newCallAdmissionStore creates the registry-owned call publication store.
func newCallAdmissionStore(redisClient *redis.Client, registryName string) *callAdmissionStore {
	return &callAdmissionStore{
		redis:  redisClient,
		prefix: "registry:" + registryName + ":call:",
	}
}

// Ensure atomically creates or attaches to one immutable call admission.
func (s *callAdmissionStore) Ensure(
	ctx context.Context,
	toolUseID, registrationToken, digest string,
	ttl time.Duration,
) (callAdmission, bool, error) {
	sum := sha256.Sum256([]byte(toolUseID + "\x00" + registrationToken))
	key := s.prefix + hex.EncodeToString(sum[:])
	value, err := ensureCallAdmissionScript.Run(
		ctx,
		s.redis,
		[]string{key},
		digest,
		ttl.Milliseconds(),
	).Slice()
	if err != nil {
		return callAdmission{}, false, fmt.Errorf("ensure call admission: %w", err)
	}
	status, err := redisResultInt64(value[0])
	if err != nil {
		return callAdmission{}, false, err
	}
	existingDigest, _ := value[1].(string)
	if status < 0 {
		return callAdmission{}, false, fmt.Errorf(
			"%w: existing digest %s",
			errCallAdmissionConflict,
			existingDigest,
		)
	}
	return callAdmission{
		key:    key,
		digest: digest,
	}, status == 1, nil
}

// ClaimPublication obtains exact temporary ownership when the current state
// requires an initial publish or one not-yet-handled overload retry.
func (s *callAdmissionStore) ClaimPublication(
	ctx context.Context,
	admission callAdmission,
	overloadEventID string,
) (*callPublication, bool, error) {
	owner := uuid.NewString()
	lockKey := admission.key + ":publishing"
	claimed, err := s.redis.SetNX(ctx, lockKey, owner, callPublicationLockTTL).Result()
	if err != nil {
		return nil, false, fmt.Errorf("claim call publication: %w", err)
	}
	if !claimed {
		return nil, false, nil
	}
	publication := &callPublication{
		store: s,
		key:   lockKey,
		owner: owner,
	}
	state, err := s.redis.HMGet(
		ctx,
		admission.key,
		"digest",
		"published",
		"overload",
	).Result()
	if err != nil {
		return nil, false, errors.Join(err, publication.Release(ctx))
	}
	digest, _ := state[0].(string)
	published, _ := state[1].(string)
	handledOverload, _ := state[2].(string)
	if digest != admission.digest {
		return nil, false, errors.Join(
			errors.New("call admission changed while publication was claimed"),
			publication.Release(ctx),
		)
	}
	if overloadEventID == "" && published == "1" ||
		overloadEventID != "" && handledOverload == overloadEventID {
		return nil, false, publication.Release(ctx)
	}
	return publication, true, nil
}

// MarkPublished commits successful publication before releasing ownership.
func (p *callPublication) MarkPublished(
	ctx context.Context,
	admission callAdmission,
	overloadEventID string,
	ttl time.Duration,
) error {
	updated, err := markCallPublishedScript.Run(
		ctx,
		p.store.redis,
		[]string{admission.key},
		admission.digest,
		overloadEventID,
		ttl.Milliseconds(),
	).Int64()
	if err != nil {
		return fmt.Errorf("mark call published: %w", err)
	}
	if updated != 1 {
		return errors.New("call admission changed while publication was owned")
	}
	return nil
}

// Release relinquishes publication only when this exact owner still holds it.
func (p *callPublication) Release(ctx context.Context) error {
	if _, err := releaseCallPublicationScript.Run(
		ctx,
		p.store.redis,
		[]string{p.key},
		p.owner,
	).Int64(); err != nil {
		return fmt.Errorf("release call publication: %w", err)
	}
	return nil
}

// redisResultInt64 normalizes Lua integer replies returned by go-redis.
func redisResultInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	default:
		return 0, fmt.Errorf("unexpected Redis integer reply %T", value)
	}
}
