// Package registry uses Redis server time as the single authority for persisted
// lease deadlines and group-health freshness. This boundary keeps wall-clock
// reads out of catalog and health state transitions.
package registry

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type (
	// registryTimeSource supplies the authoritative time used by exact registry
	// state operations.
	registryTimeSource interface {
		Now(ctx context.Context) (time.Time, error)
	}

	redisTimeSource struct {
		client *redis.Client
	}
)

// newRedisTimeSource binds authoritative registry time to the configured Redis
// deployment.
func newRedisTimeSource(client *redis.Client) registryTimeSource {
	return &redisTimeSource{client: client}
}

// Now returns Redis TIME so persisted deadlines and freshness decisions are
// independent of registry-node wall clocks.
func (s *redisTimeSource) Now(ctx context.Context) (time.Time, error) {
	now, err := s.client.Time(ctx).Result()
	if err != nil {
		return time.Time{}, fmt.Errorf("read Redis server time: %w", err)
	}
	return now, nil
}
