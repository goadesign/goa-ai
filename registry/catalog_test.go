package registry

// These tests exercise provider transitions with separate state, definitions,
// and retirement history. The test store commits related changes under one lock;
// integration tests verify the corresponding Redis operations.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
)

const (
	testAdmissionRevisionA = "2026-07-23.1"
	testAdmissionRevisionB = "2026-07-23.2"
	testStaleToken         = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testIncarnationA       = "11111111-1111-4111-8111-111111111111"
	testIncarnationB       = "22222222-2222-4222-8222-222222222222"
)

type testCatalogMap struct {
	mu                     sync.RWMutex
	content                map[string]string
	definitions            map[string]string
	retiredTokens          map[string]struct{}
	clock                  registryTimeSource
	readErr                error
	snapshotErr            error
	commitErr              error
	afterExactRead         func(string)
	afterRetiredTokensRead func()
	beforeCommit           func(string, catalogWrite)
	snapshotReads          int
	definitionWrites       int
}

func TestCatalogSameTokenAddRenewReleaseRolling(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := newTestTimeSource(now)
	catalog := newToolsetCatalog(newTestCatalogMap(clock), clock)
	toolset := testCatalogToolset("test.toolset", "test", []string{"one"})

	first, err := catalog.Register(ctx, testCatalogDefinition(t, toolset), testAdmissionRevisionA, "provider-a", testIncarnationA, time.Minute)
	require.NoError(t, err)
	second, err := catalog.Register(ctx, testCatalogDefinition(t, testCatalogToolset("test.toolset", "test", []string{"one"})), testAdmissionRevisionA, "provider-b", testIncarnationB, time.Minute)
	require.NoError(t, err)
	clock.Set(now.Add(20 * time.Second))
	require.NoError(t, catalog.RenewProvider(ctx, "test.toolset", "provider-a", testIncarnationA, first.RegistrationToken, time.Minute))
	renewed, err := catalog.activeState(ctx, "test.toolset")
	require.NoError(t, err)

	assert.Equal(t, first.RegistrationToken, second.RegistrationToken)
	assert.Equal(t, first.RegisteredAt, renewed.RegisteredAt)
	assert.Equal(t, providerLease{
		ExpiresAtUnixMilli: now.Add(80 * time.Second).UnixMilli(),
	}, renewed.ProviderLeases[providerLeaseKey("provider-a", testIncarnationA)])
	assert.Contains(t, renewed.ProviderLeases, providerLeaseKey("provider-b", testIncarnationB))

	require.NoError(t, catalog.ReleaseProvider(ctx, "test.toolset", "provider-a", testIncarnationA, first.RegistrationToken))
	entry, err := catalog.ActiveRegistration(ctx, "test.toolset")
	require.NoError(t, err)
	assert.NotContains(t, entry.ProviderLeases, providerLeaseKey("provider-a", testIncarnationA))
	assert.Contains(t, entry.ProviderLeases, providerLeaseKey("provider-b", testIncarnationB))

	require.NoError(t, catalog.ReleaseProvider(ctx, "test.toolset", "provider-b", testIncarnationB, testStaleToken))
	entry, err = catalog.ActiveRegistration(ctx, "test.toolset")
	require.NoError(t, err)
	assert.Contains(t, entry.ProviderLeases, providerLeaseKey("provider-b", testIncarnationB))
}

func TestCatalogDrainFencesRoutingButPreservesSettlementLease(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := newTestTimeSource(now)
	catalog := newToolsetCatalog(newTestCatalogMap(clock), clock)
	admission, err := catalog.Register(
		ctx,
		testCatalogDefinition(t, testCatalogToolset("test.toolset", "test", nil)),
		testAdmissionRevisionA,
		"provider",
		testIncarnationA,
		time.Minute,
	)
	require.NoError(t, err)

	require.NoError(t, catalog.DrainProvider(
		ctx,
		"test.toolset",
		"provider",
		testIncarnationA,
		admission.RegistrationToken,
		time.Minute,
	))
	entry, _, err := catalog.healthEntry(ctx, "test.toolset")
	require.NoError(t, err)
	assert.Zero(t, routableProviderCount(entry, now))
	assert.Equal(t, admission.HealthEpoch+1, entry.HealthEpoch)
	lease := entry.ProviderLeases[providerLeaseKey("provider", testIncarnationA)]
	assert.True(t, lease.Draining)
	assert.Equal(t, now.Add(time.Minute).UnixMilli(), lease.ExpiresAtUnixMilli)
	active, _, err := catalog.ActiveProviderLease(
		ctx,
		"test.toolset",
		"provider",
		testIncarnationA,
		admission.RegistrationToken,
	)
	require.NoError(t, err)
	assert.True(t, active, "draining lease remains valid for terminal settlement")
	_, _, err = catalog.HealthIdentity(ctx, "test.toolset")
	require.ErrorIs(t, err, errToolsetNotFound)
	require.NoError(t, catalog.ReleaseProvider(
		ctx,
		"test.toolset",
		"provider",
		testIncarnationA,
		admission.RegistrationToken,
	))
	released, _, err := catalog.healthEntry(ctx, "test.toolset")
	require.NoError(t, err)
	assert.Equal(t, entry.HealthEpoch, released.HealthEpoch)
}

func TestCatalogReleasePrunesExpiredRoutableEpochOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := newTestTimeSource(now)
	catalog := newToolsetCatalog(newTestCatalogMap(), clock)
	admission, err := catalog.Register(
		ctx,
		testCatalogDefinition(t, testCatalogToolset("test.toolset", "test", nil)),
		testAdmissionRevisionA,
		"provider",
		testIncarnationA,
		time.Minute,
	)
	require.NoError(t, err)

	clock.Set(now.Add(time.Minute))
	require.NoError(t, catalog.ReleaseProvider(
		ctx,
		"test.toolset",
		"provider",
		testIncarnationA,
		admission.RegistrationToken,
	))
	entry, _, err := catalog.healthEntry(ctx, "test.toolset")
	require.NoError(t, err)
	assert.Equal(t, admission.HealthEpoch+1, entry.HealthEpoch)
	require.NoError(t, catalog.ReleaseProvider(
		ctx,
		"test.toolset",
		"provider",
		testIncarnationA,
		admission.RegistrationToken,
	))
	unchanged, _, err := catalog.healthEntry(ctx, "test.toolset")
	require.NoError(t, err)
	assert.Equal(t, entry.HealthEpoch, unchanged.HealthEpoch)
}

func TestAdmissionTokenBindsWireProtocolVersion(t *testing.T) {
	t.Parallel()

	fingerprint, err := toolsetSchemaFingerprint(testCatalogToolset("test.toolset", "test", nil))
	require.NoError(t, err)
	current, err := admissionRegistrationToken(
		fingerprint,
		testAdmissionRevisionA,
		toolregistry.WireProtocolVersion,
	)
	require.NoError(t, err)
	other, err := admissionRegistrationToken(
		fingerprint,
		testAdmissionRevisionA,
		toolregistry.WireProtocolVersion+1,
	)
	require.NoError(t, err)

	assert.NotEqual(t, current, other)
}

func TestSchemaFingerprintBindsExecutionPayloadSchema(t *testing.T) {
	t.Parallel()

	modelSchema := []byte(`{"type":"object","additionalProperties":false}`)
	resultSchema := []byte(`{"type":"object"}`)
	first := &genregistry.Toolset{
		Name: "test.toolset",
		Tools: []*genregistry.ToolSchema{{
			Name:                   "lookup",
			PayloadSchema:          modelSchema,
			ExecutionPayloadSchema: []byte(`{"type":"object","properties":{"cursor":{"type":"string"}}}`),
			ResultSchema:           resultSchema,
		}},
	}
	second := &genregistry.Toolset{
		Name: "test.toolset",
		Tools: []*genregistry.ToolSchema{{
			Name:                   "lookup",
			PayloadSchema:          modelSchema,
			ExecutionPayloadSchema: []byte(`{"type":"object","properties":{"cursor":{"type":"integer"}}}`),
			ResultSchema:           resultSchema,
		}},
	}

	firstFingerprint, err := toolsetSchemaFingerprint(first)
	require.NoError(t, err)
	secondFingerprint, err := toolsetSchemaFingerprint(second)
	require.NoError(t, err)
	assert.NotEqual(t, firstFingerprint, secondFingerprint)
}

func TestCatalogRejectsPersistedMismatchedWireProtocol(t *testing.T) {
	t.Parallel()

	entry, err := newToolsetCatalog(
		newTestCatalogMap(),
		newTestTimeSource(time.Unix(1_700_000_000, 0)),
	).Register(
		context.Background(),
		testCatalogDefinition(t, testCatalogToolset("test.toolset", "test", nil)),
		testAdmissionRevisionA,
		"provider",
		testIncarnationA,
		time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, toolregistry.WireProtocolVersion, entry.WireProtocolVersion)

	entry.WireProtocolVersion++
	body, err := marshalCatalogState(entry)
	require.NoError(t, err)
	_, err = parseCatalogState("test.toolset", body)
	require.ErrorContains(t, err, "invalid wire protocol version")
}

func TestCatalogValidatesEveryPersistedEntry(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	validEntry := testPersistedCatalogEntry(t, "valid.toolset", now)
	validBody, err := marshalCatalogState(validEntry.catalogState)
	require.NoError(t, err)
	numericLeaseBody := strings.Replace(
		validBody,
		fmt.Sprintf(
			`{"expires_at_unix_milli":%d,"draining":false}`,
			now.Add(time.Minute).UnixMilli(),
		),
		"123",
		1,
	)
	require.NotEqual(t, validBody, numericLeaseBody)
	unknownFieldBody := strings.TrimSuffix(validBody, "}") + `,"future_field":true}`

	tests := []struct {
		name    string
		content map[string]string
		wantErr string
	}{
		{
			name:    "empty bootstrap",
			content: map[string]string{},
		},
		{
			name: "valid catalog",
			content: map[string]string{
				toolsetCatalogKey("valid.toolset"): validBody,
			},
		},
		{
			name: "numeric provider lease",
			content: map[string]string{
				toolsetCatalogKey("valid.toolset"): numericLeaseBody,
			},
			wantErr: "provider_leases",
		},
		{
			name: "unknown persisted field",
			content: map[string]string{
				toolsetCatalogKey("valid.toolset"): unknownFieldBody,
			},
			wantErr: "unknown field",
		},
		{
			name: "unexpected key",
			content: map[string]string{
				"unexpected": validBody,
			},
			wantErr: `catalog key "unexpected" has invalid prefix`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			m := newTestCatalogMap()
			m.content = test.content
			for key := range test.content {
				m.definitions[key] = string(validEntry.Toolset.raw)
			}
			catalog := newToolsetCatalog(m, newTestTimeSource(now))
			err := catalog.validatePersistedEntries(context.Background())
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestCatalogValidationReportsEveryIncompatibleKey(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	first := testPersistedCatalogEntry(t, "first.toolset", now)
	first.WireProtocolVersion++
	firstBody, err := marshalCatalogState(first.catalogState)
	require.NoError(t, err)
	second := testPersistedCatalogEntry(t, "second.toolset", now)
	secondBody, err := marshalCatalogState(second.catalogState)
	require.NoError(t, err)
	secondBody = strings.TrimSuffix(secondBody, "}") + `,"future_field":true}`
	valid := testPersistedCatalogEntry(t, "valid.toolset", now)
	validBody, err := marshalCatalogState(valid.catalogState)
	require.NoError(t, err)
	m := newTestCatalogMap()
	m.content = map[string]string{
		toolsetCatalogKey("first.toolset"):  firstBody,
		toolsetCatalogKey("second.toolset"): secondBody,
		toolsetCatalogKey("valid.toolset"):  validBody,
	}
	m.definitions = map[string]string{
		toolsetCatalogKey("first.toolset"):  string(first.Toolset.raw),
		toolsetCatalogKey("second.toolset"): string(second.Toolset.raw),
		toolsetCatalogKey("valid.toolset"):  string(valid.Toolset.raw),
	}

	err = newToolsetCatalog(m, newTestTimeSource(now)).validatePersistedEntries(context.Background())

	require.ErrorContains(t, err, toolsetCatalogKey("first.toolset"))
	require.ErrorContains(t, err, toolsetCatalogKey("second.toolset"))
	assert.NotContains(t, err.Error(), toolsetCatalogKey("valid.toolset"))
}

func TestCatalogDelayedOldIncarnationReleaseCannotDeleteReplacement(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	catalog := newToolsetCatalog(
		newTestCatalogMap(),
		newTestTimeSource(time.Unix(1_700_000_000, 0)),
	)
	first, err := catalog.Register(
		ctx,
		testCatalogDefinition(t, testCatalogToolset("test.toolset", "test", nil)),
		testAdmissionRevisionA,
		"provider",
		testIncarnationA,
		time.Minute,
	)
	require.NoError(t, err)
	second, err := catalog.Register(
		ctx,
		testCatalogDefinition(t, testCatalogToolset("test.toolset", "test", nil)),
		testAdmissionRevisionA,
		"provider",
		testIncarnationB,
		time.Minute,
	)
	require.NoError(t, err)

	require.NoError(t, catalog.ReleaseProvider(
		ctx,
		"test.toolset",
		"provider",
		testIncarnationA,
		first.RegistrationToken,
	))
	entry, err := catalog.ActiveRegistration(ctx, "test.toolset")
	require.NoError(t, err)
	assert.NotContains(t, entry.ProviderLeases, providerLeaseKey("provider", testIncarnationA))
	assert.Contains(t, entry.ProviderLeases, providerLeaseKey("provider", testIncarnationB))
	assert.Equal(t, second.HealthEpoch, entry.HealthEpoch)
}

func TestCatalogDifferentAdmissionGracefulAndExpiryHandoff(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := newTestTimeSource(now)
	catalog := newToolsetCatalog(newTestCatalogMap(), clock)
	old, err := catalog.Register(ctx, testCatalogDefinition(t, testCatalogToolset("test.toolset", "old", nil)), testAdmissionRevisionA, "old", testIncarnationA, time.Minute)
	require.NoError(t, err)

	_, err = catalog.Register(ctx, testCatalogDefinition(t, testCatalogToolset("test.toolset", "new", nil)), testAdmissionRevisionB, "new", testIncarnationB, time.Minute)
	require.ErrorIs(t, err, errAdmissionBlocked)

	require.NoError(t, catalog.ReleaseProvider(ctx, "test.toolset", "old", testIncarnationA, old.RegistrationToken))
	next, err := catalog.Register(ctx, testCatalogDefinition(t, testCatalogToolset("test.toolset", "new", nil)), testAdmissionRevisionB, "new", testIncarnationB, time.Minute)
	require.NoError(t, err)
	assert.NotEqual(t, old.RegistrationToken, next.RegistrationToken)
	require.ErrorIs(t, catalog.Retire(ctx, "test.toolset", old.RegistrationToken), errAdmissionConflict)

	expiryCatalog := newToolsetCatalog(newTestCatalogMap(), clock)
	_, err = expiryCatalog.Register(ctx, testCatalogDefinition(t, testCatalogToolset("expiry.toolset", "old", nil)), testAdmissionRevisionA, "crashed", testIncarnationA, time.Minute)
	require.NoError(t, err)
	clock.Set(now.Add(time.Minute))
	replacement, err := expiryCatalog.Register(ctx, testCatalogDefinition(t, testCatalogToolset("expiry.toolset", "new", nil)), testAdmissionRevisionB, "new", testIncarnationB, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, map[string]providerLease{
		providerLeaseKey("new", testIncarnationB): {
			ExpiresAtUnixMilli: now.Add(2 * time.Minute).UnixMilli(),
		},
	}, replacement.ProviderLeases)
}

func TestCatalogRetirementAndFreshRevisionReturn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	catalog := newToolsetCatalog(newTestCatalogMap(), clock)
	a, err := catalog.Register(ctx, testCatalogDefinition(t, testCatalogToolset("test.toolset", "A", nil)), testAdmissionRevisionA, "a", testIncarnationA, time.Minute)
	require.NoError(t, err)
	require.NoError(t, catalog.Retire(ctx, "test.toolset", a.RegistrationToken))
	require.NoError(t, catalog.Retire(ctx, "test.toolset", a.RegistrationToken))

	_, err = catalog.Register(ctx, testCatalogDefinition(t, testCatalogToolset("test.toolset", "A", nil)), testAdmissionRevisionA, "a", testIncarnationA, time.Minute)
	require.ErrorIs(t, err, errAdmissionRetired)
	err = catalog.Retire(ctx, "test.toolset", testStaleToken)
	require.ErrorIs(t, err, errAdmissionConflict)

	require.NoError(t, catalog.ReleaseProvider(ctx, "test.toolset", "a", testIncarnationA, a.RegistrationToken))
	b, err := catalog.Register(ctx, testCatalogDefinition(t, testCatalogToolset("test.toolset", "B", nil)), testAdmissionRevisionB, "b", testIncarnationB, time.Minute)
	require.NoError(t, err)
	require.NoError(t, catalog.ReleaseProvider(ctx, "test.toolset", "b", testIncarnationB, b.RegistrationToken))
	_, err = catalog.Register(ctx, testCatalogDefinition(t, testCatalogToolset("test.toolset", "A", nil)), testAdmissionRevisionA, "a-again", testIncarnationA, time.Minute)
	require.ErrorIs(t, err, errAdmissionRetired)
	a2, err := catalog.Register(ctx, testCatalogDefinition(t, testCatalogToolset("test.toolset", "A", nil)), "2026-07-23.3", "a2", testIncarnationA, time.Minute)
	require.NoError(t, err)
	assert.NotEqual(t, a.RegistrationToken, a2.RegistrationToken)
	retired, err := catalog.store.RetiredTokens(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{a.RegistrationToken, b.RegistrationToken}, retired)
}

func TestCatalogConcurrentCandidatesSerialize(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := newTestTimeSource(now)
	catalog := newToolsetCatalog(newTestCatalogMap(), clock)
	_, err := catalog.Register(
		ctx,
		testCatalogDefinition(t, testCatalogToolset("test.toolset", "old", nil)),
		testAdmissionRevisionA,
		"old",
		testIncarnationA,
		toolregistry.MinProviderLeaseDuration,
	)
	require.NoError(t, err)
	clock.Set(now.Add(toolregistry.MinProviderLeaseDuration))

	type result struct {
		entry catalogState
		err   error
	}
	results := make(chan result, 2)
	for _, candidate := range []struct {
		description string
		revision    string
		provider    string
	}{
		{description: "B", revision: testAdmissionRevisionB, provider: "b"},
		{description: "C", revision: "2026-07-23.3", provider: "c"},
	} {
		definition := testCatalogDefinition(t, testCatalogToolset("test.toolset", candidate.description, nil))
		go func() {
			entry, registerErr := catalog.Register(
				ctx,
				definition,
				candidate.revision,
				candidate.provider,
				testIncarnationB,
				time.Minute,
			)
			results <- result{entry: entry, err: registerErr}
		}()
	}
	var succeeded, blocked int
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			succeeded++
		case errors.Is(result.err, errAdmissionBlocked):
			blocked++
		default:
			require.NoError(t, result.err)
		}
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, blocked)
}

func TestCatalogRejectsLeaseDurationAndDeadlineOverflow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	toolset := testCatalogToolset("test.toolset", "test", nil)
	catalog := newToolsetCatalog(
		newTestCatalogMap(),
		newTestTimeSource(time.Unix(1_700_000_000, 0)),
	)
	_, err := catalog.Register(
		ctx,
		testCatalogDefinition(t, toolset),
		testAdmissionRevisionA,
		"provider-a",
		testIncarnationA,
		toolregistry.MaxProviderLeaseDuration+time.Nanosecond,
	)
	require.ErrorContains(t, err, "provider lease duration")

	overflowCatalog := newToolsetCatalog(
		newTestCatalogMap(),
		newTestTimeSource(time.UnixMilli(
			math.MaxInt64-toolregistry.MinProviderLeaseDuration.Milliseconds()+1,
		)),
	)
	_, err = overflowCatalog.Register(
		ctx,
		testCatalogDefinition(t, testCatalogToolset("overflow.toolset", "test", nil)),
		testAdmissionRevisionA,
		"provider-a",
		testIncarnationA,
		toolregistry.MinProviderLeaseDuration,
	)
	require.ErrorContains(t, err, "overflows Unix milliseconds")
}

func TestCatalogFailedReplacementPreservesAllRecords(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	catalog, store, _ := testDefinitionCatalog(t)
	old, err := catalog.ActiveRegistration(ctx, "tools")
	require.NoError(t, err)
	require.NoError(t, catalog.ReleaseProvider(ctx, "tools", "provider", testIncarnationA, old.RegistrationToken))
	before, exists := store.Get(toolsetCatalogKey("tools"))
	require.True(t, exists)
	next := testDefinitionToolset()
	next.Tools[0].ConsumerContract.Title = "replacement"
	definition := testCatalogDefinition(t, next)
	failure := errors.New("commit unavailable")
	store.mu.Lock()
	store.commitErr = failure
	store.mu.Unlock()

	_, err = catalog.Register(ctx, definition, testAdmissionRevisionB, "provider", testIncarnationB, time.Minute)

	require.ErrorIs(t, err, failure)
	after, exists := store.Get(toolsetCatalogKey("tools"))
	require.True(t, exists)
	assert.Equal(t, before, after)
	assert.Equal(t, string(old.Toolset.raw), store.definitions[toolsetCatalogKey("tools")])
	assert.Empty(t, store.retiredTokens)
	assert.Same(t, old.Toolset, catalog.definitions["tools"])
	require.NoError(t, catalog.validatePersistedEntries(ctx))

	_, err = catalog.Register(ctx, definition, testAdmissionRevisionB, "provider", testIncarnationB, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, string(definition.raw), store.definitions[toolsetCatalogKey("tools")])
	assert.Contains(t, store.retiredTokens, old.RegistrationToken)
}

func TestCatalogRegisterRechecksPermanentRetirementAtCommit(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newTestCatalogMap()
	catalog := newToolsetCatalog(store, newTestTimeSource(time.Unix(1_700_000_000, 0)))
	definition := testCatalogDefinition(t, testCatalogToolset("tools", "test", nil))
	token, err := admissionRegistrationToken(definition.fingerprint, testAdmissionRevisionA, toolregistry.WireProtocolVersion)
	require.NoError(t, err)
	store.beforeCommit = func(_ string, write catalogWrite) {
		assert.Equal(t, token, write.CandidateToken)
		store.mu.Lock()
		store.retiredTokens[token] = struct{}{}
		store.mu.Unlock()
	}

	_, err = catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)

	require.ErrorIs(t, err, errAdmissionRetired)
	assert.Empty(t, store.content)
	assert.Empty(t, store.definitions)
	assert.Empty(t, catalog.definitions)
	assert.Contains(t, store.retiredTokens, token)
}

func TestCatalogRegistrationRejectsIncompletePair(t *testing.T) {
	t.Parallel()
	for _, missing := range []string{"state", "definition"} {
		t.Run(missing, func(t *testing.T) {
			t.Parallel()
			catalog, store, _ := testDefinitionCatalog(t)
			key := toolsetCatalogKey("tools")
			if missing == "state" {
				delete(store.content, key)
			} else {
				delete(store.definitions, key)
			}
			before, existed := store.Get(key)

			_, err := catalog.Register(t.Context(), testCatalogDefinition(t, testDefinitionToolset()),
				testAdmissionRevisionA, "provider", testIncarnationB, time.Minute)

			require.ErrorContains(t, err, "CATALOGINCOMPLETE")
			after, exists := store.Get(key)
			assert.Equal(t, existed, exists)
			assert.Equal(t, before, after)
			assert.Equal(t, 1, store.definitionWrites)
		})
	}
}

func TestCatalogStartupAcceptsConcurrentRetirement(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	writer, store, clock := testDefinitionCatalog(t)
	current, err := writer.activeState(ctx, "tools")
	require.NoError(t, err)
	retiredBetweenReads := false
	store.mu.Lock()
	store.afterRetiredTokensRead = func() {
		store.mu.Lock()
		store.afterRetiredTokensRead = nil
		store.mu.Unlock()
		require.NoError(t, writer.Retire(ctx, "tools", current.RegistrationToken))
		retiredBetweenReads = true
	}
	store.mu.Unlock()

	cold := newToolsetCatalog(store, clock)
	require.NoError(t, cold.validatePersistedEntries(ctx))

	require.True(t, retiredBetweenReads)
	stateRaw, _, retired, exists, err := store.Snapshot(ctx, toolsetCatalogKey("tools"))
	require.NoError(t, err)
	require.True(t, exists)
	assert.True(t, retired)
	state, err := parseCatalogState("tools", stateRaw)
	require.NoError(t, err)
	assert.Equal(t, catalogEntryRetired, state.State)
	assert.Equal(t, current.RegistrationToken, state.RegistrationToken)
	assert.Equal(t, current.ProviderLeases, state.ProviderLeases)
}

func testCatalogToolset(name, description string, tags []string) *genregistry.Toolset {
	return &genregistry.Toolset{
		Name:        name,
		Description: &description,
		Tags:        tags,
		Tools:       []*genregistry.ToolSchema{},
	}
}

// testPersistedCatalogEntry builds a validated state/definition pair for startup tests.
func testPersistedCatalogEntry(t *testing.T, name string, now time.Time) catalogEntry {
	t.Helper()

	catalog := newToolsetCatalog(newTestCatalogMap(), newTestTimeSource(now))
	_, err := catalog.Register(
		context.Background(),
		testCatalogDefinition(t, testCatalogToolset(name, "test", nil)),
		testAdmissionRevisionA,
		"provider",
		testIncarnationA,
		time.Minute,
	)
	require.NoError(t, err)
	entry, err := catalog.ActiveRegistration(t.Context(), name)
	require.NoError(t, err)
	return entry
}

// testCatalogDefinition validates test inputs at the same boundary as the service
// and computes the fingerprint before passing the private definition to Register.
func testCatalogDefinition(t testing.TB, toolset *genregistry.Toolset) *catalogToolset {
	t.Helper()
	validator := newSchemaValidator()
	require.NoError(t, validator.ValidateToolSchemas(toolset.Tools))
	fingerprint, err := toolsetSchemaFingerprint(toolset)
	require.NoError(t, err)
	definition, err := newCatalogToolset(toolset, fingerprint, validator)
	require.NoError(t, err)
	return definition
}

// newTestCatalogMap keeps the shared helper name while implementing catalogStore.
// Lease-extension tests supply the catalog clock for the commit-time expiry check.
func newTestCatalogMap(clocks ...registryTimeSource) *testCatalogMap {
	clock := registryTimeSource(newTestTimeSource(time.Now()))
	if len(clocks) > 0 {
		clock = clocks[0]
	}
	return &testCatalogMap{
		content:       make(map[string]string),
		definitions:   make(map[string]string),
		retiredTokens: make(map[string]struct{}),
		clock:         clock,
	}
}

func (m *testCatalogMap) Get(key string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	value, exists := m.content[key]
	return value, exists
}

func (m *testCatalogMap) Read(ctx context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return "", false, err
	}
	if m.readErr != nil {
		err := m.readErr
		m.readErr = nil
		m.mu.Unlock()
		return "", false, err
	}
	value, exists := m.content[key]
	if _, definitionExists := m.definitions[key]; exists && !definitionExists {
		m.mu.Unlock()
		return "", false, errors.New("CATALOGINCOMPLETE")
	}
	afterRead := m.afterExactRead
	m.mu.Unlock()
	if afterRead != nil {
		afterRead(key)
	}
	return value, exists, nil
}

func (m *testCatalogMap) Snapshot(ctx context.Context, key string) (string, string, bool, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", "", false, false, err
	}
	m.snapshotReads++
	if m.snapshotErr != nil {
		return "", "", false, false, m.snapshotErr
	}
	state, stateExists := m.content[key]
	definition, definitionExists := m.definitions[key]
	if stateExists != definitionExists {
		return "", "", false, false, errors.New("CATALOGINCOMPLETE")
	}
	if !stateExists {
		return "", "", false, false, nil
	}
	// Extract only the token, as the Redis script does; the catalog still owns
	// strict validation of every persisted state field after this read.
	var identity struct {
		RegistrationToken string `json:"registration_token"`
	}
	if err := json.Unmarshal([]byte(state), &identity); err != nil {
		return "", "", false, false, err
	}
	_, retired := m.retiredTokens[identity.RegistrationToken]
	return state, definition, retired, true, nil
}

func (m *testCatalogMap) Keys(ctx context.Context) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(m.content))
	for key := range m.content {
		keys = append(keys, key)
	}
	return keys, nil
}

func (m *testCatalogMap) DefinitionKeys(ctx context.Context) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(m.definitions))
	for key := range m.definitions {
		keys = append(keys, key)
	}
	return keys, nil
}

func (m *testCatalogMap) Retired(ctx context.Context, token string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, retired := m.retiredTokens[token]
	return retired, nil
}

func (m *testCatalogMap) RetiredTokens(ctx context.Context) ([]string, error) {
	m.mu.RLock()
	if err := ctx.Err(); err != nil {
		m.mu.RUnlock()
		return nil, err
	}
	tokens := make([]string, 0, len(m.retiredTokens))
	for token := range m.retiredTokens {
		tokens = append(tokens, token)
	}
	afterRead := m.afterRetiredTokensRead
	m.mu.RUnlock()
	if afterRead != nil {
		afterRead()
	}
	return tokens, nil
}

// Commit checks the previous state, paired definition, retirement, and live lease
// expiry before changing any record. The hook runs outside the lock so tests can
// schedule a competing transition immediately before the atomic write.
func (m *testCatalogMap) Commit(ctx context.Context, key, previous string, next catalogWrite) (bool, error) {
	m.mu.RLock()
	beforeCommit := m.beforeCommit
	m.mu.RUnlock()
	if beforeCommit != nil {
		beforeCommit(key, next)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if m.commitErr != nil {
		err := m.commitErr
		m.commitErr = nil
		return false, err
	}
	current, exists := m.content[key]
	if current != previous {
		return false, nil
	}
	_, definitionExists := m.definitions[key]
	if exists != definitionExists {
		return false, errors.New("CATALOGINCOMPLETE")
	}
	if _, retired := m.retiredTokens[next.CandidateToken]; next.CandidateToken != "" && retired {
		return false, errAdmissionRetired
	}
	if next.LiveLease != "" {
		if !exists {
			return false, errProviderLeaseLost
		}
		var state catalogState
		if err := json.Unmarshal([]byte(current), &state); err != nil {
			return false, err
		}
		now, err := m.clock.Now(ctx)
		if err != nil {
			return false, err
		}
		lease, exists := state.ProviderLeases[next.LiveLease]
		if !exists || lease.ExpiresAtUnixMilli <= now.UnixMilli() {
			return false, errProviderLeaseLost
		}
	}
	if next.Definition != "" {
		m.definitions[key] = next.Definition
		m.definitionWrites++
	}
	if next.RetireToken != "" {
		m.retiredTokens[next.RetireToken] = struct{}{}
	}
	m.content[key] = next.State
	return true, nil
}
