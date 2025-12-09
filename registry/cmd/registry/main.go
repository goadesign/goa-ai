// Command registry runs the internal tool registry gRPC server.
//
// The registry acts as both a catalog and gateway — agents discover toolsets
// through the registry and invoke tools through it.
//
// # Clustering
//
// Multiple nodes with the same REGISTRY_NAME and REDIS_URL form a cluster,
// automatically sharing state and coordinating health checks. Clients can
// connect to any node and see the same registry state.
//
// # Configuration
//
// Environment variables:
//
//	REGISTRY_ADDR          - gRPC listen address (default: ":9090")
//	REGISTRY_NAME          - Registry cluster name (default: "registry")
//	REDIS_URL              - Redis connection URL (default: "localhost:6379")
//	REDIS_PASSWORD         - Redis password (optional)
//	PING_INTERVAL          - Health check ping interval (default: "10s")
//	MISSED_PING_THRESHOLD  - Missed pings before unhealthy (default: 3)
//
// # Example
//
// Single node:
//
//	REDIS_URL=localhost:6379 go run ./registry/cmd/registry
//
// Multi-node cluster (run on different hosts/ports):
//
//	REGISTRY_NAME=prod REGISTRY_ADDR=:9090 REDIS_URL=redis:6379 ./registry
//	REGISTRY_NAME=prod REGISTRY_ADDR=:9091 REDIS_URL=redis:6379 ./registry
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"goa.design/goa-ai/registry"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx := context.Background()

	// Load configuration from environment.
	addr := envOr("REGISTRY_ADDR", ":9090")
	name := envOr("REGISTRY_NAME", "registry")
	redisURL := envOr("REDIS_URL", "localhost:6379")
	redisPassword := os.Getenv("REDIS_PASSWORD")
	pingInterval := envDurationOr("PING_INTERVAL", 10*time.Second)
	missedPingThreshold := envIntOr("MISSED_PING_THRESHOLD", 3)

	// Connect to Redis.
	rdb := redis.NewClient(&redis.Options{
		Addr:     redisURL,
		Password: redisPassword,
	})
	defer func() {
		if err := rdb.Close(); err != nil {
			log.Printf("close redis: %v", err)
		}
	}()

	// Verify Redis connection.
	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("connect to redis: %w", err)
	}

	// Create the registry.
	reg, err := registry.New(ctx, registry.Config{
		Redis:               rdb,
		Name:                name,
		PingInterval:        pingInterval,
		MissedPingThreshold: missedPingThreshold,
	})
	if err != nil {
		return fmt.Errorf("create registry: %w", err)
	}

	// Run the registry server.
	log.Printf("starting registry on %s (name=%s)", addr, name)
	if err := reg.Run(ctx, addr); err != nil {
		return fmt.Errorf("run registry: %w", err)
	}

	return nil
}

// envOr returns the environment variable value or a default.
func envOr(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// envIntOr returns the environment variable as int or a default.
func envIntOr(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}

// envDurationOr returns the environment variable as duration or a default.
func envDurationOr(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultVal
}
