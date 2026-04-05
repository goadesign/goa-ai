// Package registry provides the internal tool registry gateway service.
//
// This package contains the server-side implementation of the registry,
// which runs as a standalone service. It includes:
//
//   - Service implementation (service.go) — gRPC service handlers
//   - Toolset catalog (catalog.go) — Pulse-backed metadata persistence
//   - Health tracking (health_tracker.go) — provider liveness detection
//   - Stream management (stream_manager.go) — Pulse stream handling
//   - Generated code (gen/) — Goa-generated types and gRPC transport
//   - Design (design/) — Goa DSL service definition
//
// # Multi-Node Clustering
//
// Multiple registry nodes can participate in the same logical registry by
// using the same Name in their Config and connecting to the same Redis instance.
// Nodes with the same name automatically:
//
//   - Share toolset registrations via replicated maps
//   - Coordinate health check pings via distributed tickers (only one node pings at a time)
//   - Share provider health state across all nodes
//
// This enables horizontal scaling and high availability. Clients can connect
// to any node and see the same registry state.
//
// For generated agent-side clients emitted from DSL `Registry(...)` declarations,
// see `gen/<svc>/registry/<name>/`. For the shared wire protocol used by this
// service, providers, and executors, see `runtime/toolregistry`.
package registry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	registrypb "goa.design/goa-ai/registry/gen/grpc/registry/pb"
	grpcserver "goa.design/goa-ai/registry/gen/grpc/registry/server"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/pulse/pool"
	"goa.design/pulse/rmap"
	"google.golang.org/grpc"
)

type (
	// Registry is the main entry point for the internal tool registry.
	// It manages all components required for multi-node operation including
	// Pulse streams, replicated maps, and distributed tickers.
	Registry struct {
		service       *Service
		pulseClient   clientspulse.Client
		healthMap     *rmap.Map
		registryMap   *rmap.Map
		poolNode      *pool.Node
		healthTracker HealthTracker
		streamManager StreamManager
		redis         *redis.Client
	}

	// Config configures the registry service.
	Config struct {
		// Redis is the Redis client for Pulse operations. Required.
		Redis *redis.Client
		// Name is the registry name used to derive Pulse resource names.
		// Multiple nodes with the same Name and Redis connection form a cluster,
		// sharing state and coordinating health checks automatically.
		//
		// The pool, health map, and registry map names are derived as:
		//   - Pool: "<name>"
		//   - Health map: "<name>:health"
		//   - Registry map: "<name>:toolsets"
		//
		// Defaults to "registry" if not provided.
		Name string
		// Logger receives health tracker logs (pings, transitions, failures).
		// When nil, health tracking logs are suppressed.
		Logger telemetry.Logger
		// PingInterval is the interval between health check pings.
		// Defaults to 10 seconds if not provided.
		PingInterval time.Duration
		// MissedPingThreshold is the number of consecutive missed pings
		// before marking a toolset as unhealthy.
		// Defaults to 3 if not provided.
		MissedPingThreshold int
		// ResultStreamMappingTTL is the TTL for tool_use_id to stream_id mappings.
		// ResultStreamTTL is the TTL for per-call result streams in Redis.
		// Defaults to 15 minutes if not provided.
		ResultStreamTTL time.Duration
		// PoolNodeOptions are additional options for the Pulse pool node.
		PoolNodeOptions []pool.NodeOption
	}
)

// New creates a new Registry with all components wired together.
// The registry connects to Redis for Pulse stream operations and creates
// replicated maps for cross-node state synchronization.
//
// The caller is responsible for calling Close() when done to release resources.
func New(ctx context.Context, cfg Config) (*Registry, error) {
	if cfg.Redis == nil {
		return nil, fmt.Errorf("redis client is required")
	}

	// Apply defaults and derive Pulse resource names.
	name := cfg.Name
	if name == "" {
		name = "registry"
	}
	poolName := name
	healthMapName := name + ":health"
	registryMapName := name + ":toolsets"

	// Create Pulse client for stream operations.
	pulseClient, err := clientspulse.New(clientspulse.Options{
		Redis: cfg.Redis,
	})
	if err != nil {
		return nil, fmt.Errorf("create pulse client: %w", err)
	}

	// Create Pulse replicated maps for shared state.
	healthMap, err := rmap.Join(ctx, healthMapName, cfg.Redis)
	if err != nil {
		return nil, fmt.Errorf("join health map: %w", err)
	}

	registryMap, err := rmap.Join(ctx, registryMapName, cfg.Redis)
	if err != nil {
		healthMap.Close()
		return nil, fmt.Errorf("join registry map: %w", err)
	}

	// Create Pulse pool node for distributed tickers.
	poolNode, err := pool.AddNode(ctx, poolName, cfg.Redis, cfg.PoolNodeOptions...)
	if err != nil {
		healthMap.Close()
		registryMap.Close()
		return nil, fmt.Errorf("add pool node: %w", err)
	}

	// Create stream manager.
	streamManager := NewStreamManager(pulseClient)

	// Build health tracker options.
	var healthOpts []HealthTrackerOption
	if cfg.PingInterval > 0 {
		healthOpts = append(healthOpts, WithPingInterval(cfg.PingInterval))
	}
	if cfg.MissedPingThreshold > 0 {
		healthOpts = append(healthOpts, WithMissedPingThreshold(cfg.MissedPingThreshold))
	}
	if cfg.Logger != nil {
		healthOpts = append(healthOpts, WithHealthLogger(cfg.Logger))
	}

	// Create health tracker.
	healthTracker, err := NewHealthTracker(streamManager, healthMap, registryMap, poolNode, healthOpts...)
	if err != nil {
		healthMap.Close()
		registryMap.Close()
		closeErr := poolNode.Close(ctx)
		return nil, errors.Join(fmt.Errorf("create health tracker: %w", err), closeErr)
	}

	// Create the authoritative toolset catalog.
	catalog := newToolsetCatalog(registryMap)

	// Create the service.
	service, err := newService(serviceOptions{
		catalog:         catalog,
		StreamManager:   streamManager,
		HealthTracker:   healthTracker,
		PulseClient:     pulseClient,
		ResultStreamTTL: cfg.ResultStreamTTL,
	})
	if err != nil {
		htCloseErr := healthTracker.Close()
		healthMap.Close()
		registryMap.Close()
		poolCloseErr := poolNode.Close(ctx)
		return nil, errors.Join(fmt.Errorf("create service: %w", err), htCloseErr, poolCloseErr)
	}

	return &Registry{
		service:       service,
		pulseClient:   pulseClient,
		healthMap:     healthMap,
		registryMap:   registryMap,
		poolNode:      poolNode,
		healthTracker: healthTracker,
		streamManager: streamManager,
		redis:         cfg.Redis,
	}, nil
}

// Service returns the registry service implementation.
// This implements the genregistry.Service interface.
func (r *Registry) Service() *Service {
	return r.service
}

// Close releases all resources held by the registry.
// It stops all ping loops, cleans up result streams, closes Pulse components,
// and closes Redis connections.
//
// The caller is responsible for closing the Redis client if they own it.
// This method does not close the Redis client passed in Config.
func (r *Registry) Close(ctx context.Context) error {
	var errs []error

	// Stop all ping loops via health tracker.
	if r.healthTracker != nil {
		if err := r.healthTracker.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close health tracker: %w", err))
		}
	}

	// Close Pulse pool node.
	if r.poolNode != nil {
		if err := r.poolNode.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close pool node: %w", err))
		}
	}

	// Close rmap instances.
	if r.healthMap != nil {
		r.healthMap.Close()
	}
	if r.registryMap != nil {
		r.registryMap.Close()
	}

	// Close Pulse client.
	if r.pulseClient != nil {
		if err := r.pulseClient.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close pulse client: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("close registry: %v", errs)
	}
	return nil
}

// Run starts the gRPC server and blocks until the context is canceled or
// a termination signal is received. It handles graceful shutdown automatically.
//
// The addr parameter specifies the network address to listen on (e.g., ":9090").
// Optional gRPC server options can be passed to customize the server.
//
// Example:
//
//	reg, _ := registry.New(ctx, registry.Config{Redis: rdb})
//	if err := reg.Run(ctx, ":9090"); err != nil {
//	    log.Fatal(err)
//	}
func (r *Registry) Run(ctx context.Context, addr string, opts ...grpc.ServerOption) error {
	// Create listener.
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	// Create gRPC server with the registry service.
	grpcServer := grpc.NewServer(opts...)
	endpoints := genregistry.NewEndpoints(r.service)
	registrypb.RegisterRegistryServer(grpcServer, grpcserver.New(endpoints, nil))

	// Set up signal handling for graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Channel to capture server errors.
	errCh := make(chan error, 1)

	// Start serving in a goroutine.
	go func() {
		errCh <- grpcServer.Serve(lis)
	}()

	// Wait for shutdown signal or context cancellation.
	select {
	case <-ctx.Done():
	case sig := <-sigCh:
		_ = sig // Signal received, proceed to shutdown.
	case err := <-errCh:
		// Server stopped unexpectedly.
		return err
	}

	// Graceful shutdown: stop accepting new connections and drain existing ones.
	grpcServer.GracefulStop()

	// Close registry resources.
	if err := r.Close(ctx); err != nil {
		return fmt.Errorf("close registry: %w", err)
	}

	return nil
}
