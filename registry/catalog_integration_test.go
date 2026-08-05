//go:build integration

package registry

// Redis-backed admission tests prove exact CAS serialization across registry
// replicas, stopped same-admission rebootstrap, and live recovery after
// store-owned rmap/stream destruction.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/rmap"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
)

var (
	testRedisClient    *redis.Client
	testRedisContainer testcontainers.Container
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	var err error
	testRedisContainer, err = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "redis:7-alpine",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForLog("Ready to accept connections"),
		},
		Started: true,
	})
	if err != nil {
		fmt.Printf("integration tests require Docker: failed to start Redis: %v\n", err)
		os.Exit(1)
	}
	host, err := testRedisContainer.Host(ctx)
	if err != nil {
		fmt.Printf("failed to get Redis host: %v\n", err)
		os.Exit(1)
	}
	port, err := testRedisContainer.MappedPort(ctx, "6379")
	if err != nil {
		fmt.Printf("failed to get Redis port: %v\n", err)
		os.Exit(1)
	}
	testRedisClient = redis.NewClient(&redis.Options{Addr: host + ":" + port.Port()})
	if err := testRedisClient.Ping(ctx).Err(); err != nil {
		fmt.Printf("failed to ping Redis: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	if err := testRedisClient.Close(); err != nil {
		fmt.Printf("failed to close Redis client: %v\n", err)
	}
	if err := testRedisContainer.Terminate(ctx); err != nil {
		fmt.Printf("failed to terminate Redis: %v\n", err)
	}
	os.Exit(code)
}

func TestMultiNodeAdmissionHandoff(t *testing.T) {
	ctx := context.Background()
	name := fmt.Sprintf("catalog-handoff-%d", time.Now().UnixNano())
	map1, err := rmap.Join(ctx, name, testRedisClient)
	require.NoError(t, err)
	t.Cleanup(map1.Close)
	map2, err := rmap.Join(ctx, name, testRedisClient)
	require.NoError(t, err)
	t.Cleanup(map2.Close)
	clock := newRedisTimeSource(testRedisClient)
	catalog1 := newToolsetCatalog(authoritativeCatalogMap{Map: map1, rdb: testRedisClient}, clock)
	catalog2 := newToolsetCatalog(authoritativeCatalogMap{Map: map2, rdb: testRedisClient}, clock)

	old, err := catalog1.Register(
		ctx,
		testCatalogToolset("test.toolset", "old", nil),
		testAdmissionRevisionA,
		"old-a",
		testIncarnationA,
		time.Minute,
	)
	require.NoError(t, err)
	_, err = catalog2.Register(
		ctx,
		testCatalogToolset("test.toolset", "new", nil),
		testAdmissionRevisionB,
		"new-a",
		testIncarnationB,
		time.Minute,
	)
	require.ErrorIs(t, err, errAdmissionBlocked)

	require.NoError(t, catalog1.ReleaseProvider(ctx, "test.toolset", "old-a", testIncarnationA, old.RegistrationToken))
	replacement, err := catalog2.Register(
		ctx,
		testCatalogToolset("test.toolset", "new", nil),
		testAdmissionRevisionB,
		"new-a",
		testIncarnationB,
		time.Minute,
	)
	require.NoError(t, err)
	assert.NotEqual(t, old.RegistrationToken, replacement.RegistrationToken)

	require.NoError(t, catalog1.ReleaseProvider(ctx, "test.toolset", "old-a", testIncarnationA, old.RegistrationToken))
	active, err := catalog1.ActiveRegistration(ctx, "test.toolset")
	require.NoError(t, err)
	assert.Equal(t, replacement.RegistrationToken, active.RegistrationToken)
	assert.Contains(t, active.ProviderLeases, providerLeaseKey("new-a", testIncarnationB))
}

func TestStoppedRedisRebootstrapReconstructsSameAdmission(t *testing.T) {
	ctx := context.Background()
	name := fmt.Sprintf("catalog-loss-%d", time.Now().UnixNano())
	m, err := rmap.Join(ctx, name, testRedisClient)
	require.NoError(t, err)
	clock := newRedisTimeSource(testRedisClient)
	catalog := newToolsetCatalog(authoritativeCatalogMap{Map: m, rdb: testRedisClient}, clock)
	first, err := catalog.Register(
		ctx,
		testCatalogToolset("test.toolset", "same", nil),
		testAdmissionRevisionA,
		"provider-a",
		testIncarnationA,
		time.Minute,
	)
	require.NoError(t, err)
	m.Close()

	require.NoError(t, testRedisClient.FlushDB(ctx).Err())
	recoveredMap, err := rmap.Join(ctx, name+"-recovered", testRedisClient)
	require.NoError(t, err)
	t.Cleanup(recoveredMap.Close)
	recovered, err := newToolsetCatalog(authoritativeCatalogMap{Map: recoveredMap, rdb: testRedisClient}, clock).Register(
		ctx,
		testCatalogToolset("test.toolset", "same", nil),
		testAdmissionRevisionA,
		"provider-b",
		testIncarnationB,
		time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, first.RegistrationToken, recovered.RegistrationToken)
}

func TestResultStreamSlidingTTLRefreshesAndCleansRecreation(t *testing.T) {
	ctx := context.Background()
	name := fmt.Sprintf("result-retention-%d", time.Now().UnixNano())
	key := "pulse:stream:" + name
	const ttl = time.Second

	resultStream, err := streaming.NewStream(
		name,
		testRedisClient,
		streamopts.WithStreamSlidingTTL(ttl),
	)
	require.NoError(t, err)
	_, err = resultStream.Add(ctx, "init", []byte("{}"))
	require.NoError(t, err)
	time.Sleep(600 * time.Millisecond)
	before, err := testRedisClient.PTTL(ctx, key).Result()
	require.NoError(t, err)
	_, err = resultStream.Add(ctx, toolregistry.ResultEventKey, []byte("{}"))
	require.NoError(t, err)
	after, err := testRedisClient.PTTL(ctx, key).Result()
	require.NoError(t, err)
	assert.Greater(t, after, before)

	require.Eventually(t, func() bool {
		exists, existsErr := testRedisClient.Exists(ctx, key).Result()
		return existsErr == nil && exists == 0
	}, 3*time.Second, 25*time.Millisecond)

	recreated, err := streaming.NewStream(
		name,
		testRedisClient,
		streamopts.WithStreamSlidingTTL(ttl),
	)
	require.NoError(t, err)
	_, err = recreated.Add(ctx, toolregistry.OutputDeltaEventKey, []byte("{}"))
	require.NoError(t, err)
	recreatedTTL, err := testRedisClient.PTTL(ctx, key).Result()
	require.NoError(t, err)
	assert.Greater(t, recreatedTTL, time.Duration(0))
	assert.LessOrEqual(t, recreatedTTL, ttl)
	require.Eventually(t, func() bool {
		exists, existsErr := testRedisClient.Exists(ctx, key).Result()
		return existsErr == nil && exists == 0
	}, 3*time.Second, 25*time.Millisecond)
}

func TestResultStreamReadersReplayWithoutConsumerGroups(t *testing.T) {
	ctx := context.Background()
	name := fmt.Sprintf("result-replay-%d", time.Now().UnixNano())
	resultStream, err := streaming.NewStream(
		name,
		testRedisClient,
		streamopts.WithStreamSlidingTTL(time.Second),
	)
	require.NoError(t, err)
	_, err = resultStream.Add(ctx, toolregistry.ResultEventKey, []byte(`{"ok":true}`))
	require.NoError(t, err)

	read := func() string {
		reader, readerErr := resultStream.NewReader(
			ctx,
			streamopts.WithReaderStartAtOldest(),
			streamopts.WithReaderBlockDuration(10*time.Millisecond),
		)
		require.NoError(t, readerErr)
		defer reader.Close()
		event := <-reader.Subscribe()
		require.NotNil(t, event)
		return event.ID
	}
	first := read()
	second := read()
	assert.Equal(t, first, second)
	groups, err := testRedisClient.XInfoGroups(ctx, "pulse:stream:"+name).Result()
	require.NoError(t, err)
	assert.Empty(t, groups)
}

func TestCallAdmissionAtomicallyPublishesInitialAndOverloadOnce(t *testing.T) {
	ctx := context.Background()
	name := fmt.Sprintf("call-admission-%d", time.Now().UnixNano())
	firstStore := newCallAdmissionStore(testRedisClient, name)
	secondStore := newCallAdmissionStore(testRedisClient, name)
	catalogMap, err := rmap.Join(ctx, name+":toolsets", testRedisClient)
	require.NoError(t, err)
	t.Cleanup(catalogMap.Close)
	catalog := newToolsetCatalog(
		authoritativeCatalogMap{Map: catalogMap, rdb: testRedisClient},
		newRedisTimeSource(testRedisClient),
	)
	const (
		toolUseID = "tool-use"
		digest    = "request-digest"
		streamID  = "toolset:call-admission-test:requests"
		toolset   = "call-admission-test"
	)
	registration, err := catalog.Register(
		ctx,
		testCatalogToolset(toolset, "atomic publication", nil),
		testAdmissionRevisionA,
		"provider",
		testIncarnationA,
		time.Minute,
	)
	require.NoError(t, err)
	token := registration.RegistrationToken
	first, created, err := firstStore.Ensure(
		ctx, toolset, toolUseID, token, digest, time.Second, 5*time.Second,
		outcomeUnknownPayload(token, toolUseID),
	)
	require.NoError(t, err)
	require.True(t, created)
	second, created, err := secondStore.Ensure(
		ctx, toolset, toolUseID, token, digest, time.Second, 5*time.Second,
		outcomeUnknownPayload(token, toolUseID),
	)
	require.NoError(t, err)
	require.False(t, created)

	type publicationResult struct {
		eventID string
		err     error
	}
	publish := func(overloadEventID string) []publicationResult {
		results := make(chan publicationResult, 2)
		for _, admission := range []callAdmission{first, second} {
			go func(admission callAdmission) {
				eventID, publishErr := publishAdmittedBounded(
					ctx,
					testRedisClient,
					streamID,
					maxQueuedToolCalls,
					string(toolregistry.MessageTypeCall),
					[]byte(`{"type":"call"}`),
					admission,
					overloadEventID,
				)
				results <- publicationResult{eventID: eventID, err: publishErr}
			}(admission)
		}
		return []publicationResult{<-results, <-results}
	}

	initial := publish("")
	require.NoError(t, initial[0].err)
	require.NoError(t, initial[1].err)
	assert.Equal(t, initial[0].eventID, initial[1].eventID)
	assert.EqualValues(t, 1, testRedisClient.XLen(ctx, pulseStreamKeyPrefix+streamID).Val())

	overload := publish("overload-1")
	require.NoError(t, overload[0].err)
	require.NoError(t, overload[1].err)
	assert.Equal(t, overload[0].eventID, overload[1].eventID)
	assert.NotEqual(t, initial[0].eventID, overload[0].eventID)
	assert.EqualValues(t, 2, testRedisClient.XLen(ctx, pulseStreamKeyPrefix+streamID).Val())

	resultStreamID := toolregistry.ResultStreamID(toolUseID)
	claimDisposition, err := firstStore.Claim(
		ctx,
		toolset,
		toolUseID,
		token,
		token,
		providerLeaseKey("provider", testIncarnationA),
		overload[0].eventID,
		resultStreamID,
		[]byte(`{"stale":true}`),
	)
	require.NoError(t, err)
	require.Equal(t, callClaimExecute, claimDisposition)
	terminal := []byte(fmt.Sprintf(
		`{"registration_token":%q,"tool_use_id":%q,"result_json":{"ok":true}}`,
		token,
		toolUseID,
	))
	completions := make(chan error, 2)
	for _, store := range []*callAdmissionStore{firstStore, secondStore} {
		go func(store *callAdmissionStore) {
			completions <- store.Complete(
				ctx,
				toolset,
				toolUseID,
				token,
				token,
				providerLeaseKey("provider", testIncarnationA),
				overload[0].eventID,
				resultStreamID,
				terminal,
			)
		}(store)
	}
	require.NoError(t, <-completions)
	require.NoError(t, <-completions)
	assert.EqualValues(t, 1, testRedisClient.XLen(ctx, pulseStreamKeyPrefix+resultStreamID).Val())
	err = firstStore.Complete(
		ctx,
		toolset,
		toolUseID,
		token,
		token,
		providerLeaseKey("provider", testIncarnationA),
		overload[0].eventID,
		resultStreamID,
		[]byte(`{"different":true}`),
	)
	require.ErrorIs(t, err, errCallTerminalConflict)
	assert.EqualValues(t, 1, testRedisClient.XLen(ctx, pulseStreamKeyPrefix+resultStreamID).Val())
	replayed, _, err := firstStore.Ensure(
		ctx, toolset, toolUseID, token, digest, time.Second, 5*time.Second,
		outcomeUnknownPayload(token, toolUseID),
	)
	require.NoError(t, err)
	assert.True(t, replayed.terminal)
	_, _, err = firstStore.Ensure(
		ctx,
		toolset,
		toolUseID,
		strings.Repeat("b", 64),
		"different-generation-request",
		time.Second,
		5*time.Second,
		outcomeUnknownPayload(strings.Repeat("b", 64), toolUseID),
	)
	require.ErrorIs(t, err, errCallAdmissionConflict)

	const drainingToolUseID = "draining-tool-use"
	drainingAdmission, _, err := firstStore.Ensure(
		ctx,
		toolset,
		drainingToolUseID,
		token,
		"draining-request-digest",
		time.Second,
		5*time.Second,
		outcomeUnknownPayload(token, drainingToolUseID),
	)
	require.NoError(t, err)
	require.NoError(t, catalog.DrainProvider(
		ctx,
		toolset,
		"provider",
		testIncarnationA,
		token,
		time.Minute,
	))
	_, err = publishAdmittedBounded(
		ctx,
		testRedisClient,
		streamID,
		maxQueuedToolCalls,
		string(toolregistry.MessageTypeCall),
		[]byte(`{"type":"call"}`),
		drainingAdmission,
		"",
	)
	require.ErrorIs(t, err, errRoutingUnavailable)

	attached, err := firstStore.Attach(ctx, toolset, toolUseID, digest)
	require.NoError(t, err)
	disposition, err := firstStore.Claim(
		ctx,
		toolset,
		toolUseID,
		token,
		token,
		providerLeaseKey("provider", testIncarnationA),
		initial[0].eventID,
		toolregistry.ResultStreamID(toolUseID),
		[]byte(`{"stale":true}`),
	)
	require.NoError(t, err)
	assert.Equal(t, callClaimTerminal, disposition)
	require.Eventually(t, func() bool {
		exists, existsErr := testRedisClient.Exists(ctx, attached.key).Result()
		return existsErr == nil && exists == 0
	}, 7*time.Second, 25*time.Millisecond)
	disposition, err = firstStore.Claim(
		ctx,
		toolset,
		toolUseID,
		token,
		token,
		providerLeaseKey("provider", testIncarnationA),
		initial[0].eventID,
		toolregistry.ResultStreamID(toolUseID),
		[]byte(`{"stale":true}`),
	)
	require.NoError(t, err)
	assert.Equal(t, callClaimExpired, disposition)
}

func TestCallAdmissionRetryCannotCreateExpiredAdmission(t *testing.T) {
	ctx := context.Background()
	name := fmt.Sprintf("call-admission-attach-%d", time.Now().UnixNano())
	store := newCallAdmissionStore(testRedisClient, name)
	const (
		toolUseID = "missing-tool-use"
		token     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		toolset   = "missing-toolset"
	)

	_, err := store.Attach(ctx, toolset, toolUseID, "request-digest")
	require.ErrorIs(t, err, errCallAdmissionNotFound)
	exists, err := testRedisClient.Exists(ctx, store.admissionKey(toolUseID)).Result()
	require.NoError(t, err)
	assert.Zero(t, exists)
}

func TestRedisConcurrentRenewalReplacementAndCandidates(t *testing.T) {
	ctx := context.Background()
	name := fmt.Sprintf("catalog-races-%d", time.Now().UnixNano())
	map1, err := rmap.Join(ctx, name, testRedisClient)
	require.NoError(t, err)
	t.Cleanup(map1.Close)
	map2, err := rmap.Join(ctx, name, testRedisClient)
	require.NoError(t, err)
	t.Cleanup(map2.Close)
	clock := newRedisTimeSource(testRedisClient)
	catalog1 := newToolsetCatalog(authoritativeCatalogMap{Map: map1, rdb: testRedisClient}, clock)
	catalog2 := newToolsetCatalog(authoritativeCatalogMap{Map: map2, rdb: testRedisClient}, clock)
	oldRenewal, err := catalog1.Register(
		ctx,
		testCatalogToolset("renewal-race", "old", nil),
		testAdmissionRevisionA,
		"old",
		testIncarnationA,
		time.Minute,
	)
	require.NoError(t, err)
	require.NoError(t, catalog1.ReleaseProvider(
		ctx,
		"renewal-race",
		"old",
		testIncarnationA,
		oldRenewal.RegistrationToken,
	))

	renewalErrs := make(chan error, 2)
	go func() {
		_, registerErr := catalog1.Register(
			ctx,
			testCatalogToolset("renewal-race", "old", nil),
			testAdmissionRevisionA,
			"old",
			testIncarnationA,
			time.Minute,
		)
		renewalErrs <- registerErr
	}()
	go func() {
		_, registerErr := catalog2.Register(
			ctx,
			testCatalogToolset("renewal-race", "new", nil),
			testAdmissionRevisionB,
			"new",
			testIncarnationB,
			time.Minute,
		)
		renewalErrs <- registerErr
	}()
	var succeeded, fenced int
	for range 2 {
		switch registerErr := <-renewalErrs; {
		case registerErr == nil:
			succeeded++
		case errors.Is(registerErr, errAdmissionRetired),
			errors.Is(registerErr, errAdmissionBlocked):
			fenced++
		default:
			require.NoError(t, registerErr)
		}
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, fenced)

	oldCandidate, err := catalog1.Register(
		ctx,
		testCatalogToolset("candidate-race", "old", nil),
		testAdmissionRevisionA,
		"old",
		testIncarnationA,
		time.Minute,
	)
	require.NoError(t, err)
	require.NoError(t, catalog1.ReleaseProvider(
		ctx,
		"candidate-race",
		"old",
		testIncarnationA,
		oldCandidate.RegistrationToken,
	))
	candidateErrs := make(chan error, 2)
	for _, candidate := range []struct {
		description string
		revision    string
		provider    string
	}{
		{description: "B", revision: testAdmissionRevisionB, provider: "b"},
		{description: "C", revision: "2026-07-23.3", provider: "c"},
	} {
		go func() {
			_, registerErr := catalog2.Register(
				ctx,
				testCatalogToolset("candidate-race", candidate.description, nil),
				candidate.revision,
				candidate.provider,
				testIncarnationB,
				time.Minute,
			)
			candidateErrs <- registerErr
		}()
	}
	succeeded = 0
	var blocked int
	for range 2 {
		switch registerErr := <-candidateErrs; {
		case registerErr == nil:
			succeeded++
		case errors.Is(registerErr, errAdmissionBlocked):
			blocked++
		default:
			require.NoError(t, registerErr)
		}
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, blocked)
}

func TestLiveRegistryRecoversOwnedStateLossSameName(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()
	registryName := fmt.Sprintf("live-loss-%d", time.Now().UnixNano())
	reg, err := New(ctx, Config{
		Redis:               rdb,
		Name:                registryName,
		PingInterval:        30 * time.Millisecond,
		MissedPingThreshold: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(ctx)) })
	payload := &genregistry.RegisterPayload{
		Name:                  "live.toolset",
		ProviderID:            "provider-a",
		ProviderIncarnationID: testIncarnationA,
		AdmissionRevision:     testAdmissionRevisionA,
		WireProtocolVersion:   toolregistry.WireProtocolVersion,
		Tools:                 []*genregistry.ToolSchema{},
	}
	first, err := reg.Service().Register(ctx, payload)
	require.NoError(t, err)
	stream, err := reg.pulseClient.Stream(toolregistry.ToolsetStreamID(payload.Name))
	require.NoError(t, err)
	sink, err := stream.NewSink(ctx, "loss-observer")
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		sink.Close(closeCtx)
	})
	events := sink.Subscribe()
	waitForRegistrationPing(t, events, first.RegistrationToken)

	require.NoError(t, reg.registryMap.Destroy(ctx))
	// Simulate Redis losing the toolset stream's state (data, groups, and
	// lifecycle) rather than an intentional Stream.Destroy: destruction leaves
	// a terminal tombstone by contract, while genuine loss is what Pulse's
	// genesis re-establishment and this registry's self-healing recover from.
	streamKey := "pulse:stream:" + toolregistry.ToolsetStreamID(payload.Name)
	require.NoError(t, rdb.Del(ctx,
		streamKey,
		streamKey+":lifecycle",
		streamKey+":sink-recovery:1",
	).Err())
	recovered, err := reg.Service().Register(ctx, payload)
	require.NoError(t, err)
	assert.Equal(t, first.RegistrationToken, recovered.RegistrationToken)
	recoveredSink, err := stream.NewSink(ctx, "loss-observer-recovered")
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		recoveredSink.Close(closeCtx)
	})
	waitForRegistrationPing(t, recoveredSink.Subscribe(), recovered.RegistrationToken)
}

func waitForRegistrationPing(
	t *testing.T,
	events <-chan *streaming.Event,
	registrationToken string,
) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event, ok := <-events:
			require.True(t, ok, "observer sink closed during Redis recovery")
			var message toolregistry.ToolCallMessage
			require.NoError(t, json.Unmarshal(event.Payload, &message))
			if message.Type == toolregistry.MessageTypePing &&
				message.RegistrationToken == registrationToken {
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for recovered registration ping")
		}
	}
}

func getRedis(t *testing.T) *redis.Client {
	t.Helper()
	require.NoError(t, testRedisClient.FlushDB(context.Background()).Err())
	return testRedisClient
}
