package registry

// These tests pin the single-record admission state machine independently from
// rmap replication. Integration tests exercise the same CAS transitions across
// real registry replicas.

import (
	"context"
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
	"goa.design/pulse/rmap"
)

const (
	testAdmissionRevisionA = "2026-07-23.1"
	testAdmissionRevisionB = "2026-07-23.2"
	testStaleToken         = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testIncarnationA       = "11111111-1111-4111-8111-111111111111"
	testIncarnationB       = "22222222-2222-4222-8222-222222222222"
)

type testCatalogMap struct {
	mu            sync.RWMutex
	content       map[string]string
	events        chan rmap.EventKind
	testAndSetErr error
	subscribe     func() <-chan rmap.EventKind
}

func TestCatalogSameTokenAddRenewReleaseRolling(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := newTestTimeSource(now)
	catalog := newToolsetCatalog(newTestCatalogMap(), clock)
	toolset := testCatalogToolset("test.toolset", "test", []string{"one"})

	first, err := catalog.Register(ctx, toolset, testAdmissionRevisionA, "provider-a", testIncarnationA, time.Minute)
	require.NoError(t, err)
	second, err := catalog.Register(ctx, testCatalogToolset("test.toolset", "test", []string{"one"}), testAdmissionRevisionA, "provider-b", testIncarnationB, time.Minute)
	require.NoError(t, err)
	clock.Set(now.Add(20 * time.Second))
	renewed, err := catalog.Register(ctx, testCatalogToolset("test.toolset", "test", []string{"one"}), testAdmissionRevisionA, "provider-a", testIncarnationA, time.Minute)
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
	catalog := newToolsetCatalog(newTestCatalogMap(), clock)
	admission, err := catalog.Register(
		ctx,
		testCatalogToolset("test.toolset", "test", nil),
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
		testCatalogToolset("test.toolset", "test", nil),
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

func TestCatalogRejectsPersistedMismatchedWireProtocol(t *testing.T) {
	t.Parallel()

	entry, err := newToolsetCatalog(
		newTestCatalogMap(),
		newTestTimeSource(time.Unix(1_700_000_000, 0)),
	).Register(
		context.Background(),
		testCatalogToolset("test.toolset", "test", nil),
		testAdmissionRevisionA,
		"provider",
		testIncarnationA,
		time.Minute,
	)
	require.NoError(t, err)
	assert.Equal(t, toolregistry.WireProtocolVersion, entry.WireProtocolVersion)

	entry.WireProtocolVersion++
	body, err := marshalCatalogEntry(entry)
	require.NoError(t, err)
	_, err = parseCatalogEntry("test.toolset", body)
	require.ErrorContains(t, err, "invalid wire protocol version")
}

func TestCatalogValidatesEveryPersistedEntry(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	validEntry := testPersistedCatalogEntry(t, "valid.toolset", now)
	validBody, err := marshalCatalogEntry(validEntry)
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
	firstBody, err := marshalCatalogEntry(first)
	require.NoError(t, err)
	second := testPersistedCatalogEntry(t, "second.toolset", now)
	secondBody, err := marshalCatalogEntry(second)
	require.NoError(t, err)
	secondBody = strings.TrimSuffix(secondBody, "}") + `,"future_field":true}`
	valid := testPersistedCatalogEntry(t, "valid.toolset", now)
	validBody, err := marshalCatalogEntry(valid)
	require.NoError(t, err)
	m := newTestCatalogMap()
	m.content = map[string]string{
		toolsetCatalogKey("first.toolset"):  firstBody,
		toolsetCatalogKey("second.toolset"): secondBody,
		toolsetCatalogKey("valid.toolset"):  validBody,
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
		testCatalogToolset("test.toolset", "test", nil),
		testAdmissionRevisionA,
		"provider",
		testIncarnationA,
		time.Minute,
	)
	require.NoError(t, err)
	second, err := catalog.Register(
		ctx,
		testCatalogToolset("test.toolset", "test", nil),
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
	old, err := catalog.Register(ctx, testCatalogToolset("test.toolset", "old", nil), testAdmissionRevisionA, "old", testIncarnationA, time.Minute)
	require.NoError(t, err)

	_, err = catalog.Register(ctx, testCatalogToolset("test.toolset", "new", nil), testAdmissionRevisionB, "new", testIncarnationB, time.Minute)
	require.ErrorIs(t, err, errAdmissionBlocked)

	require.NoError(t, catalog.ReleaseProvider(ctx, "test.toolset", "old", testIncarnationA, old.RegistrationToken))
	next, err := catalog.Register(ctx, testCatalogToolset("test.toolset", "new", nil), testAdmissionRevisionB, "new", testIncarnationB, time.Minute)
	require.NoError(t, err)
	assert.NotEqual(t, old.RegistrationToken, next.RegistrationToken)
	require.ErrorIs(t, catalog.Retire(ctx, "test.toolset", old.RegistrationToken), errAdmissionConflict)

	expiryCatalog := newToolsetCatalog(newTestCatalogMap(), clock)
	_, err = expiryCatalog.Register(ctx, testCatalogToolset("expiry.toolset", "old", nil), testAdmissionRevisionA, "crashed", testIncarnationA, time.Minute)
	require.NoError(t, err)
	clock.Set(now.Add(time.Minute))
	replacement, err := expiryCatalog.Register(ctx, testCatalogToolset("expiry.toolset", "new", nil), testAdmissionRevisionB, "new", testIncarnationB, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, map[string]providerLease{
		providerLeaseKey("new", testIncarnationB): {
			ExpiresAtUnixMilli: now.Add(2 * time.Minute).UnixMilli(),
		},
	}, replacement.ProviderLeases)
}

func TestCatalogOldRenewalAndReplacementSerialize(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := newTestTimeSource(now)
	catalog := newToolsetCatalog(newTestCatalogMap(), clock)
	_, err := catalog.Register(
		ctx,
		testCatalogToolset("test.toolset", "old", nil),
		testAdmissionRevisionA,
		"old",
		testIncarnationA,
		toolregistry.MinProviderLeaseDuration,
	)
	require.NoError(t, err)
	clock.Set(now.Add(toolregistry.MinProviderLeaseDuration))

	errs := make(chan error, 2)
	go func() {
		_, registerErr := catalog.Register(
			ctx,
			testCatalogToolset("test.toolset", "old", nil),
			testAdmissionRevisionA,
			"old",
			testIncarnationA,
			time.Minute,
		)
		errs <- registerErr
	}()
	go func() {
		_, registerErr := catalog.Register(
			ctx,
			testCatalogToolset("test.toolset", "new", nil),
			testAdmissionRevisionB,
			"new",
			testIncarnationB,
			time.Minute,
		)
		errs <- registerErr
	}()

	var succeeded, fenced int
	for range 2 {
		err := <-errs
		if err == nil {
			succeeded++
			continue
		}
		require.True(
			t,
			errors.Is(err, errAdmissionRetired) || errors.Is(err, errAdmissionBlocked),
			"renewal/replacement loser must be fenced: %v",
			err,
		)
		fenced++
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, fenced)
	entry, err := catalog.ActiveRegistration(ctx, "test.toolset")
	require.NoError(t, err)
	assert.Len(t, entry.ProviderLeases, 1)
}

func TestCatalogRetirementAndFreshRevisionReturn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	catalog := newToolsetCatalog(newTestCatalogMap(), clock)
	a, err := catalog.Register(ctx, testCatalogToolset("test.toolset", "A", nil), testAdmissionRevisionA, "a", testIncarnationA, time.Minute)
	require.NoError(t, err)
	require.NoError(t, catalog.Retire(ctx, "test.toolset", a.RegistrationToken))
	require.NoError(t, catalog.Retire(ctx, "test.toolset", a.RegistrationToken))

	_, err = catalog.Register(ctx, testCatalogToolset("test.toolset", "A", nil), testAdmissionRevisionA, "a", testIncarnationA, time.Minute)
	require.ErrorIs(t, err, errAdmissionRetired)
	err = catalog.Retire(ctx, "test.toolset", testStaleToken)
	require.ErrorIs(t, err, errAdmissionConflict)

	require.NoError(t, catalog.ReleaseProvider(ctx, "test.toolset", "a", testIncarnationA, a.RegistrationToken))
	b, err := catalog.Register(ctx, testCatalogToolset("test.toolset", "B", nil), testAdmissionRevisionB, "b", testIncarnationB, time.Minute)
	require.NoError(t, err)
	require.NoError(t, catalog.ReleaseProvider(ctx, "test.toolset", "b", testIncarnationB, b.RegistrationToken))
	_, err = catalog.Register(ctx, testCatalogToolset("test.toolset", "A", nil), testAdmissionRevisionA, "a-again", testIncarnationA, time.Minute)
	require.ErrorIs(t, err, errAdmissionRetired)
	a2, err := catalog.Register(ctx, testCatalogToolset("test.toolset", "A", nil), "2026-07-23.3", "a2", testIncarnationA, time.Minute)
	require.NoError(t, err)
	assert.NotEqual(t, a.RegistrationToken, a2.RegistrationToken)
	assert.Contains(t, a2.RetiredTokens, a.RegistrationToken)
	assert.Contains(t, a2.RetiredTokens, b.RegistrationToken)
}

func TestCatalogConcurrentCandidatesSerialize(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	clock := newTestTimeSource(now)
	catalog := newToolsetCatalog(newTestCatalogMap(), clock)
	_, err := catalog.Register(
		ctx,
		testCatalogToolset("test.toolset", "old", nil),
		testAdmissionRevisionA,
		"old",
		testIncarnationA,
		toolregistry.MinProviderLeaseDuration,
	)
	require.NoError(t, err)
	clock.Set(now.Add(toolregistry.MinProviderLeaseDuration))

	type result struct {
		entry catalogEntry
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
		go func() {
			entry, registerErr := catalog.Register(
				ctx,
				testCatalogToolset("test.toolset", candidate.description, nil),
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

func TestCatalogRedisLossRecoversSameAdmissionIdentity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m := newTestCatalogMap()
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	catalog := newToolsetCatalog(m, clock)
	first, err := catalog.Register(ctx, testCatalogToolset("test.toolset", "same", nil), testAdmissionRevisionA, "a", testIncarnationA, time.Minute)
	require.NoError(t, err)

	m.mu.Lock()
	clear(m.content)
	m.mu.Unlock()

	recovered, err := catalog.Register(ctx, testCatalogToolset("test.toolset", "same", nil), testAdmissionRevisionA, "b", testIncarnationB, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, first.RegistrationToken, recovered.RegistrationToken)
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
		toolset,
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
		testCatalogToolset("overflow.toolset", "test", nil),
		testAdmissionRevisionA,
		"provider-a",
		testIncarnationA,
		toolregistry.MinProviderLeaseDuration,
	)
	require.ErrorContains(t, err, "overflows Unix milliseconds")
}

func testCatalogToolset(name, description string, tags []string) *genregistry.Toolset {
	return &genregistry.Toolset{
		Name:        name,
		Description: &description,
		Tags:        tags,
		Tools:       []*genregistry.ToolSchema{},
	}
}

// testPersistedCatalogEntry builds one canonical active record for startup
// validation tests without depending on a pre-populated catalog map.
func testPersistedCatalogEntry(t *testing.T, name string, now time.Time) catalogEntry {
	t.Helper()

	catalog := newToolsetCatalog(newTestCatalogMap(), newTestTimeSource(now))
	entry, err := catalog.Register(
		context.Background(),
		testCatalogToolset(name, "test", nil),
		testAdmissionRevisionA,
		"provider",
		testIncarnationA,
		time.Minute,
	)
	require.NoError(t, err)
	return entry
}

func newTestCatalogMap() *testCatalogMap {
	return &testCatalogMap{
		content: make(map[string]string),
		events:  make(chan rmap.EventKind, 64),
	}
}

func (m *testCatalogMap) Get(key string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	value, exists := m.content[key]
	return value, exists
}

func (m *testCatalogMap) Keys() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]string, 0, len(m.content))
	for key := range m.content {
		keys = append(keys, key)
	}
	return keys
}

func (m *testCatalogMap) AuthoritativeKeys(context.Context) ([]string, error) {
	return m.Keys(), nil
}

func (m *testCatalogMap) Subscribe() <-chan rmap.EventKind {
	if m.subscribe != nil {
		return m.subscribe()
	}
	return m.events
}

func (m *testCatalogMap) Unsubscribe(<-chan rmap.EventKind) {}

func (m *testCatalogMap) SetIfNotExists(_ context.Context, key, value string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.content[key]; exists {
		return false, nil
	}
	m.content[key] = value
	return true, nil
}

func (m *testCatalogMap) TestAndSetEx(
	_ context.Context,
	key, test, value string,
) (string, bool, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.testAndSetErr != nil {
		err := m.testAndSetErr
		m.testAndSetErr = nil
		return "", false, false, err
	}
	current, exists := m.content[key]
	if !exists {
		return "", false, false, nil
	}
	if current != test {
		return current, true, false, nil
	}
	m.content[key] = value
	return current, true, true, nil
}

func (m *testCatalogMap) TestAndDeleteEx(
	_ context.Context,
	key, test string,
) (string, bool, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, exists := m.content[key]
	if !exists {
		return "", false, false, nil
	}
	if current != test {
		return current, true, false, nil
	}
	delete(m.content, key)
	return current, true, true, nil
}
