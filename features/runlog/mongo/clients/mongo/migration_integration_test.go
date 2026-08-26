//go:build integration

// These tests run the direct no-traffic migration against a real MongoDB
// replica set. They verify the storage validator, private migration state, and
// transactional sequence allocation used by the serving client.
package mongo

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"goa.design/goa-ai/runtime/agent/runlog"
)

func TestDirectMigrationMongoContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	mongoClient := startMigrationMongoReplicaSet(t, ctx)

	t.Run("malformed session identity preflight", func(t *testing.T) {
		tests := []struct {
			name  string
			value any
			omit  bool
		}{
			{name: "absent", omit: true},
			{name: "null", value: nil},
			{name: "wrong type", value: int32(7)},
		}
		for index, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				databaseName := fmt.Sprintf("migration_invalid_%d", index)
				event := legacyMongoEvent("run", "event", time.Unix(1, 0).UTC())
				if !test.omit {
					event["session_id"] = test.value
				}
				_, err := mongoClient.Database(databaseName).
					Collection(defaultCollection).
					InsertOne(ctx, event)
				require.NoError(t, err)

				_, err = Migrate(ctx, MigrationOptions{
					Client:   mongoClient,
					Database: databaseName,
					Apply:    true,
				})

				require.ErrorContains(t, err, "non-string session_id")
				count, countErr := mongoClient.Database(databaseName).
					Collection(defaultCollection+schemaCollectionSuffix).
					CountDocuments(ctx, bson.D{})
				require.NoError(t, countErr)
				assert.Zero(t, count)
			})
		}
	})

	const databaseName = "migration_direct"
	database := mongoClient.Database(databaseName)
	events := database.Collection(defaultCollection)
	_, err := events.InsertMany(ctx, []any{
		legacyMongoEventWithSession("run-1", "session-1", "event-1", time.Unix(1, 0).UTC()),
		legacyMongoEventWithSession("run-2", "session-1", "event-2", time.Unix(2, 0).UTC()),
		legacyMongoEventWithSession("run-3", "", "event-3", time.Unix(3, 0).UTC()),
	})
	require.NoError(t, err)

	report, err := Migrate(ctx, MigrationOptions{
		Client:   mongoClient,
		Database: databaseName,
		Apply:    true,
	})
	require.NoError(t, err)
	require.True(t, report.Applied)
	require.Equal(t, 3, report.BackfillEvents)

	var migrated []eventDocument
	cursor, err := events.Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	require.NoError(t, err)
	require.NoError(t, cursor.All(ctx, &migrated))
	require.Len(t, migrated, 3)
	assert.Equal(t, "session:session-1", migrated[0].Stream)
	assert.EqualValues(t, 1, migrated[0].Sequence)
	assert.Equal(t, "session:session-1", migrated[1].Stream)
	assert.EqualValues(t, 2, migrated[1].Sequence)
	assert.Equal(t, "run:run-3", migrated[2].Stream)
	assert.EqualValues(t, 1, migrated[2].Sequence)

	store := mongoMigrationStore{
		database:   database,
		collection: defaultCollection,
		events:     events,
		sequences:  database.Collection(defaultCollection + sequenceCollectionSuffix),
		bindings:   database.Collection(defaultCollection + bindingCollectionSuffix),
		schemas:    database.Collection(defaultCollection + schemaCollectionSuffix),
	}
	require.NoError(t, requireStorageContract(ctx, store))
	assertMongoMigrationDocument(
		t,
		ctx,
		store.sequences,
		bson.M{"_id": "session:session-1"},
		sequenceDocument{Stream: "session:session-1", Sequence: 2},
	)
	assertMongoMigrationDocument(
		t,
		ctx,
		store.bindings,
		bson.M{"_id": "run-2"},
		runBindingDocument{RunID: "run-2", Stream: "session:session-1"},
	)
	assertMongoMigrationDocument(
		t,
		ctx,
		store.schemas,
		bson.M{"_id": schemaSentinelID},
		schemaDocument{Name: schemaSentinelID, Version: schemaVersion},
	)

	_, err = store.schemas.DeleteOne(ctx, bson.M{"_id": schemaSentinelID})
	require.NoError(t, err)
	rerun, err := Migrate(ctx, MigrationOptions{
		Client:   mongoClient,
		Database: databaseName,
		Apply:    true,
	})
	require.NoError(t, err)
	assert.True(t, rerun.Applied)
	assert.Zero(t, rerun.BackfillEvents)

	runlogClient, err := New(Options{
		Client:   mongoClient,
		Database: databaseName,
		Timeout:  30 * time.Second,
	})
	require.NoError(t, err)
	const appendCount = 16
	sequences := make([]int, appendCount)
	errs := make([]error, appendCount)
	var group sync.WaitGroup
	for index := range appendCount {
		group.Add(1)
		go func() {
			defer group.Done()
			event := &runlog.Event{
				EventKey:  fmt.Sprintf("concurrent-%d", index),
				RunID:     fmt.Sprintf("concurrent-run-%d", index),
				SessionID: "session-1",
				Type:      "test",
				Timestamp: time.Unix(int64(10+index), 0).UTC(),
			}
			_, errs[index] = runlogClient.Append(ctx, event)
			if errs[index] == nil {
				_, errs[index] = fmt.Sscan(event.ID, &sequences[index])
			}
		}()
	}
	group.Wait()
	for _, appendErr := range errs {
		require.NoError(t, appendErr)
	}
	sort.Ints(sequences)
	for index, sequence := range sequences {
		assert.Equal(t, index+3, sequence)
	}

	t.Run("strict validation requires string session identity", func(t *testing.T) {
		tests := []struct {
			name  string
			value any
			omit  bool
		}{
			{name: "absent", omit: true},
			{name: "null", value: nil},
			{name: "wrong type", value: int32(7)},
		}
		for index, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				event := legacyMongoEvent("invalid-session", fmt.Sprintf("invalid-session-%d", index), time.Now().UTC())
				event["stream"] = "run:invalid-session"
				event["sequence"] = int64(index + 1)
				if !test.omit {
					event["session_id"] = test.value
				}

				_, insertErr := events.InsertOne(ctx, event)

				require.Error(t, insertErr)
			})
		}
	})

	_, err = events.InsertOne(ctx, eventDocument{
		Stream:    "session:session-1",
		Sequence:  0,
		EventKey:  "malformed",
		RunID:     "malformed",
		SessionID: "session-1",
		Type:      "test",
		Timestamp: time.Now().UTC(),
	})
	require.Error(t, err)
}

// startMigrationMongoReplicaSet starts one MongoDB node, initializes it as a
// replica set, and returns a direct client that supports transactions.
func startMigrationMongoReplicaSet(
	t *testing.T,
	ctx context.Context,
) *mongodriver.Client {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "mongo:8.2",
			ExposedPorts: []string{"27017/tcp"},
			Cmd:          []string{"--replSet", "rs0", "--bind_ip_all"},
			WaitingFor: wait.ForLog("Waiting for connections").
				WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, container.Terminate(context.Background()))
	})
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "27017")
	require.NoError(t, err)
	uri := fmt.Sprintf(
		"mongodb://%s:%s/?directConnection=true",
		host,
		port.Port(),
	)
	client, err := mongodriver.Connect(options.Client().ApplyURI(uri))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, client.Disconnect(context.Background()))
	})
	err = client.Database("admin").RunCommand(ctx, bson.D{{
		Key: "replSetInitiate",
		Value: bson.D{
			{Key: "_id", Value: "rs0"},
			{Key: "members", Value: bson.A{bson.D{
				{Key: "_id", Value: 0},
				{Key: "host", Value: "localhost:27017"},
			}}},
		},
	}}).Err()
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var hello struct {
			WritablePrimary bool `bson:"isWritablePrimary"`
		}
		err := client.Database("admin").
			RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).
			Decode(&hello)
		return err == nil && hello.WritablePrimary
	}, 30*time.Second, 100*time.Millisecond)
	return client
}

// legacyMongoEvent returns one row without the sequence fields introduced by
// the migration. The caller supplies session_id separately when testing its
// raw BSON type.
func legacyMongoEvent(runID, eventKey string, timestamp time.Time) bson.M {
	return bson.M{
		"event_key": eventKey,
		"run_id":    runID,
		"agent_id":  "agent",
		"turn_id":   "",
		"type":      "test",
		"payload":   []byte(nil),
		"timestamp": timestamp,
	}
}

// legacyMongoEventWithSession adds the string session identity required by
// migration preflight to one legacy row.
func legacyMongoEventWithSession(
	runID, sessionID, eventKey string,
	timestamp time.Time,
) bson.M {
	event := legacyMongoEvent(runID, eventKey, timestamp)
	event["session_id"] = sessionID
	return event
}

// assertMongoMigrationDocument loads one private migration record and compares
// it with the exact expected value.
func assertMongoMigrationDocument[T any](
	t *testing.T,
	ctx context.Context,
	collection *mongodriver.Collection,
	filter bson.M,
	want T,
) {
	t.Helper()
	var got T
	require.NoError(t, collection.FindOne(ctx, filter).Decode(&got))
	assert.Equal(t, want, got)
}
