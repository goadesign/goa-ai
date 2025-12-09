package mongo

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	genregistry "goa.design/goa-ai/registry/gen/registry"
)

var (
	testMongoClient    *mongo.Client
	testMongoContainer testcontainers.Container
	skipMongoTests     bool
)

func setupMongoDB() {
	ctx := context.Background()

	var containerErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				containerErr = fmt.Errorf("docker not available: %v", r)
			}
		}()
		req := testcontainers.ContainerRequest{
			Image:        "mongo:7",
			ExposedPorts: []string{"27017/tcp"},
			WaitingFor:   wait.ForLog("Waiting for connections"),
			Tmpfs:        map[string]string{"/data/db": "rw"},
		}
		testMongoContainer, containerErr = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
	}()

	if containerErr != nil {
		fmt.Printf("Docker not available, MongoDB tests will be skipped: %v\n", containerErr)
		skipMongoTests = true
		return
	}

	host, err := testMongoContainer.Host(ctx)
	if err != nil {
		fmt.Printf("Failed to get container host: %v\n", err)
		skipMongoTests = true
		return
	}

	port, err := testMongoContainer.MappedPort(ctx, "27017")
	if err != nil {
		fmt.Printf("Failed to get container port: %v\n", err)
		skipMongoTests = true
		return
	}

	uri := fmt.Sprintf("mongodb://%s:%s", host, port.Port())
	testMongoClient, err = mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		fmt.Printf("Failed to connect to MongoDB: %v\n", err)
		skipMongoTests = true
		return
	}

	if err := testMongoClient.Ping(ctx, nil); err != nil {
		fmt.Printf("Failed to ping MongoDB: %v\n", err)
		skipMongoTests = true
		return
	}
}

func getMongoStore(t *testing.T) *Store {
	t.Helper()
	if skipMongoTests {
		t.Skip("Docker not available, skipping MongoDB test")
	}
	collection := testMongoClient.Database("registry_test").Collection(t.Name())
	if err := collection.Drop(context.Background()); err != nil {
		t.Fatalf("failed to drop collection: %v", err)
	}
	return New(collection)
}

// TestMongoDBPersistenceRoundTrip verifies Property 11: MongoDB persistence round-trip.
// **Feature: internal-tool-registry, Property 11: MongoDB persistence round-trip**
func TestMongoDBPersistenceRoundTrip(t *testing.T) {
	if testMongoClient == nil && !skipMongoTests {
		setupMongoDB()
	}
	if skipMongoTests {
		t.Skip("Docker not available, skipping MongoDB test")
	}

	collection := testMongoClient.Database("registry_test").Collection(t.Name())
	ctx := context.Background()
	defer func() { _ = collection.Drop(ctx) }()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("toolsets persist across store recreation", prop.ForAll(
		func(toolsets []*genregistry.Toolset) bool {
			if err := collection.Drop(ctx); err != nil {
				return false
			}

			store1 := New(collection)
			for _, ts := range toolsets {
				if err := store1.SaveToolset(ctx, ts); err != nil {
					return false
				}
			}

			store2 := New(collection)
			restored, err := store2.ListAll(ctx)
			if err != nil {
				return false
			}

			if len(restored) != len(toolsets) {
				return false
			}

			for _, original := range toolsets {
				retrieved, err := store2.GetToolset(ctx, original.Name)
				if err != nil {
					return false
				}
				if !toolsetsEqual(original, retrieved) {
					return false
				}
			}

			return true
		},
		genToolsetSlice(),
	))

	properties.TestingRun(t)
}

// TestMongoStoreRegistrationRoundTrip verifies round-trip for individual toolsets.
func TestMongoStoreRegistrationRoundTrip(t *testing.T) {
	if testMongoClient == nil && !skipMongoTests {
		setupMongoDB()
	}

	st := getMongoStore(t)
	ctx := context.Background()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("save then get returns equivalent toolset", prop.ForAll(
		func(toolset *genregistry.Toolset) bool {
			if err := st.SaveToolset(ctx, toolset); err != nil {
				return false
			}
			retrieved, err := st.GetToolset(ctx, toolset.Name)
			if err != nil {
				return false
			}
			return toolsetsEqual(toolset, retrieved)
		},
		genToolset(),
	))

	properties.TestingRun(t)
}

// TestMongoStoreTagFiltering verifies tag filtering.
func TestMongoStoreTagFiltering(t *testing.T) {
	if testMongoClient == nil && !skipMongoTests {
		setupMongoDB()
	}

	st := getMongoStore(t)
	ctx := context.Background()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("tag filter returns only toolsets with all tags", prop.ForAll(
		func(toolsets []*genregistry.Toolset, filterTags []string) bool {
			for _, ts := range toolsets {
				if err := st.SaveToolset(ctx, ts); err != nil {
					return false
				}
			}

			results, err := st.ListToolsets(ctx, filterTags)
			if err != nil {
				return false
			}

			for _, ts := range results {
				if !hasAllTags(ts.Tags, filterTags) {
					return false
				}
			}

			for _, ts := range toolsets {
				if hasAllTags(ts.Tags, filterTags) {
					if !containsToolset(results, ts.Name) {
						return false
					}
				}
			}

			return true
		},
		genToolsetSlice(),
		genTagFilter(),
	))

	properties.TestingRun(t)
}

// TestMongoStoreSearch verifies search functionality.
func TestMongoStoreSearch(t *testing.T) {
	if testMongoClient == nil && !skipMongoTests {
		setupMongoDB()
	}

	st := getMongoStore(t)
	ctx := context.Background()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("search returns toolsets matching query", prop.ForAll(
		func(toolsets []*genregistry.Toolset, query string) bool {
			for _, ts := range toolsets {
				if err := st.SaveToolset(ctx, ts); err != nil {
					return false
				}
			}

			results, err := st.SearchToolsets(ctx, query)
			if err != nil {
				return false
			}

			for _, ts := range results {
				if !matchesSearchQuery(ts, query) {
					return false
				}
			}

			for _, ts := range toolsets {
				if matchesSearchQuery(ts, query) {
					if !containsToolset(results, ts.Name) {
						return false
					}
				}
			}

			return true
		},
		genToolsetSlice(),
		genSearchQuery(),
	))

	properties.TestingRun(t)
}

// --- Helper functions ---

func toolsetsEqual(a, b *genregistry.Toolset) bool {
	if a.Name != b.Name {
		return false
	}
	if !stringPtrEqual(a.Description, b.Description) {
		return false
	}
	if !stringPtrEqual(a.Version, b.Version) {
		return false
	}
	if !stringSliceEqual(a.Tags, b.Tags) {
		return false
	}
	if a.StreamID != b.StreamID {
		return false
	}
	if a.RegisteredAt != b.RegisteredAt {
		return false
	}
	if len(a.Tools) != len(b.Tools) {
		return false
	}
	for i := range a.Tools {
		if !toolsEqual(a.Tools[i], b.Tools[i]) {
			return false
		}
	}
	return true
}

func toolsEqual(a, b *genregistry.Tool) bool {
	if a.Name != b.Name {
		return false
	}
	if !stringPtrEqual(a.Description, b.Description) {
		return false
	}
	if !reflect.DeepEqual(a.InputSchema, b.InputSchema) {
		return false
	}
	if !reflect.DeepEqual(a.OutputSchema, b.OutputSchema) {
		return false
	}
	return true
}

func stringPtrEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hasAllTags(toolsetTags, filterTags []string) bool {
	if len(filterTags) == 0 {
		return true
	}
	tagSet := make(map[string]struct{}, len(toolsetTags))
	for _, tag := range toolsetTags {
		tagSet[tag] = struct{}{}
	}
	for _, tag := range filterTags {
		if _, ok := tagSet[tag]; !ok {
			return false
		}
	}
	return true
}

func containsToolset(toolsets []*genregistry.Toolset, name string) bool {
	for _, ts := range toolsets {
		if ts.Name == name {
			return true
		}
	}
	return false
}

func matchesSearchQuery(ts *genregistry.Toolset, query string) bool {
	lowerQuery := strings.ToLower(query)
	if strings.Contains(strings.ToLower(ts.Name), lowerQuery) {
		return true
	}
	if ts.Description != nil && strings.Contains(strings.ToLower(*ts.Description), lowerQuery) {
		return true
	}
	for _, tag := range ts.Tags {
		if strings.Contains(strings.ToLower(tag), lowerQuery) {
			return true
		}
	}
	return false
}

// --- Generators ---

func genToolset() gopter.Gen {
	return gopter.CombineGens(
		genToolsetName(),
		genOptionalString(),
		genOptionalString(),
		genTags(),
		genToolSlice(),
		genStreamID(),
		genTimestamp(),
	).Map(func(vals []any) *genregistry.Toolset {
		var desc, version *string
		if vals[1] != nil {
			desc = vals[1].(*string)
		}
		if vals[2] != nil {
			version = vals[2].(*string)
		}
		return &genregistry.Toolset{
			Name:         vals[0].(string),
			Description:  desc,
			Version:      version,
			Tags:         vals[3].([]string),
			Tools:        vals[4].([]*genregistry.Tool),
			StreamID:     vals[5].(string),
			RegisteredAt: vals[6].(string),
		}
	})
}

func genToolsetSlice() gopter.Gen {
	return gen.SliceOfN(5, genToolset()).Map(func(toolsets []*genregistry.Toolset) []*genregistry.Toolset {
		seen := make(map[string]bool)
		result := make([]*genregistry.Toolset, 0, len(toolsets))
		for i, ts := range toolsets {
			if seen[ts.Name] {
				ts.Name = ts.Name + "-" + string(rune('a'+i))
			}
			seen[ts.Name] = true
			result = append(result, ts)
		}
		return result
	})
}

func genToolsetName() gopter.Gen {
	return gen.OneConstOf("data-tools", "analytics", "etl-pipeline", "search-service", "notification-tools")
}

func genOptionalString() gopter.Gen {
	return gen.PtrOf(gen.OneConstOf("A description", "Another description", "Tools for processing", "Service utilities"))
}

func genTags() gopter.Gen {
	return gen.SliceOfN(3, gen.OneConstOf("data", "etl", "analytics", "search", "notification", "api"))
}

func genTagFilter() gopter.Gen {
	return gen.SliceOfN(2, gen.OneConstOf("data", "etl", "analytics", "search"))
}

func genSearchQuery() gopter.Gen {
	return gen.OneConstOf("data", "tool", "analytics", "search", "process", "service")
}

func genToolSlice() gopter.Gen {
	return gen.SliceOfN(3, genTool()).Map(func(tools []*genregistry.Tool) []*genregistry.Tool { return tools })
}

func genTool() gopter.Gen {
	return gopter.CombineGens(genToolName(), genOptionalString(), genSchema(), genSchema()).Map(func(vals []any) *genregistry.Tool {
		var desc *string
		if vals[1] != nil {
			desc = vals[1].(*string)
		}
		return &genregistry.Tool{
			Name:         vals[0].(string),
			Description:  desc,
			InputSchema:  vals[2].([]byte),
			OutputSchema: vals[3].([]byte),
		}
	})
}

func genToolName() gopter.Gen {
	return gen.OneConstOf("analyze", "transform", "query", "notify", "search")
}

func genSchema() gopter.Gen {
	return gen.OneConstOf([]byte(`{"type":"object"}`), []byte(`{"type":"string"}`), []byte(`{"type":"array","items":{"type":"string"}}`))
}

func genStreamID() gopter.Gen {
	return gen.OneConstOf("toolset:data-tools:requests", "toolset:analytics:requests", "toolset:etl:requests")
}

func genTimestamp() gopter.Gen {
	return gen.OneConstOf("2024-01-15T10:30:00Z", "2024-02-20T14:45:00Z", "2024-03-10T08:00:00Z")
}
