package registry

// These tests advance time without changing saved state between a provider
// join's read and commit. Old health survives only continuous live membership.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const attachJoinOperation = "attach"

// observedRetryStore exposes the interval after a rejected atomic write so
// tests can inspect unchanged records or cancel before the next attempt.
type observedRetryStore struct {
	catalogStore
	retries    int
	afterRetry func()
}

func (s *observedRetryStore) Commit(ctx context.Context, key, previous string, next catalogWrite) (bool, error) {
	committed, err := s.catalogStore.Commit(ctx, key, previous, next)
	if err == nil && !committed {
		s.retries++
		if s.afterRetry != nil {
			s.afterRetry()
		}
	}
	return committed, err
}

func TestCatalogProviderJoinChecksMembershipAtCommit(t *testing.T) {
	for _, operation := range []string{attachJoinOperation, "register"} {
		for _, incarnation := range []string{testIncarnationA, testIncarnationB} {
			for _, tc := range []struct {
				name     string
				offset   time.Duration
				survivor bool
				draining bool
				gap      bool
			}{
				{name: "before expiration", offset: -time.Millisecond},
				{name: "at expiration", gap: true},
				{name: "after expiration", offset: time.Millisecond, gap: true},
				{name: "another lease survives", survivor: true},
				{name: "only draining lease survives", survivor: true, draining: true, gap: true},
			} {
				t.Run(operation+"/"+incarnation+"/"+tc.name, func(t *testing.T) {
					ctx := t.Context()
					now := time.Unix(1_700_000_000, 0)
					clock := newTestTimeSource(now)
					store := newTestCatalogMap(clock)
					catalog := newToolsetCatalog(store, clock)
					definition := testCatalogDefinition(t, testDefinitionToolset())
					first, err := catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
					require.NoError(t, err)
					if tc.survivor {
						_, err = catalog.Register(ctx, definition, testAdmissionRevisionA, "survivor", testIncarnationB, 2*time.Minute)
						require.NoError(t, err)
						if tc.draining {
							require.NoError(t, catalog.DrainProvider(ctx, "tools", "survivor", testIncarnationB, first.RegistrationToken, 2*time.Minute))
						}
					}
					clock.Set(now.Add(time.Minute - time.Second))
					require.NoError(t, catalog.RecordPong(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, first.HealthEpoch))
					previous, err := catalog.activeState(ctx, "tools")
					require.NoError(t, err)
					require.Positive(t, previous.LastPongUnixNano)
					store.beforeCommit = func(string, catalogWrite) {
						store.beforeCommit = nil
						clock.Set(now.Add(time.Minute + tc.offset))
					}
					var joined catalogState
					if operation == attachJoinOperation {
						joined, err = catalog.AttachProvider(ctx, "tools", first.RegistrationToken, "provider", incarnation, time.Minute)
					} else {
						joined, err = catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", incarnation, time.Minute)
					}
					require.NoError(t, err)
					assert.Equal(t, first.RegistrationToken, joined.RegistrationToken)
					assert.Equal(t, first.RegisteredAt, joined.RegisteredAt)
					assert.Equal(t, 1, store.definitionWrites)
					if tc.gap {
						assert.Greater(t, joined.HealthEpoch, previous.HealthEpoch)
						assert.Zero(t, joined.LastPongUnixNano)
						require.NoError(t, catalog.RecordPong(ctx, "tools", "provider", incarnation, first.RegistrationToken, previous.HealthEpoch))
						ignored, err := catalog.activeState(ctx, "tools")
						require.NoError(t, err)
						assert.Zero(t, ignored.LastPongUnixNano)
					} else {
						assert.Equal(t, previous.HealthEpoch, joined.HealthEpoch)
						assert.Equal(t, previous.LastPongUnixNano, joined.LastPongUnixNano)
					}
					require.NoError(t, catalog.RecordPong(ctx, "tools", "provider", incarnation, first.RegistrationToken, joined.HealthEpoch))
					fresh, err := catalog.activeState(ctx, "tools")
					require.NoError(t, err)
					assert.Positive(t, fresh.LastPongUnixNano)
				})
			}
		}
	}
}

func TestCatalogProviderJoinTemporalRetryFailureLeavesRecords(t *testing.T) {
	for _, operation := range []string{attachJoinOperation, "register"} {
		for _, failure := range []string{"cancellation", "storage unavailable"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				now := time.Unix(1_700_000_000, 0)
				clock := newTestTimeSource(now)
				store := newTestCatalogMap(clock)
				observed := &observedRetryStore{catalogStore: store}
				catalog := newToolsetCatalog(observed, clock)
				definition := testCatalogDefinition(t, testDefinitionToolset())
				first, err := catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
				require.NoError(t, err)
				clock.Set(now.Add(time.Minute - time.Second))
				require.NoError(t, catalog.RecordPong(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, first.HealthEpoch))
				before, exists := store.Get(toolsetCatalogKey("tools"))
				require.True(t, exists)
				store.beforeCommit = func(string, catalogWrite) {
					store.beforeCommit = nil
					clock.Set(now.Add(time.Minute))
				}
				wantErr := errors.New("catalog unavailable after temporal retry")
				observed.afterRetry = func() {
					unchanged, exists := store.Get(toolsetCatalogKey("tools"))
					assert.True(t, exists)
					assert.Equal(t, before, unchanged)
					if failure == "cancellation" {
						wantErr = context.Canceled
						cancel()
					} else {
						store.readErr = wantErr
					}
				}
				if operation == attachJoinOperation {
					_, err = catalog.AttachProvider(ctx, "tools", first.RegistrationToken, "provider", testIncarnationB, time.Minute)
				} else {
					_, err = catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationB, time.Minute)
				}
				require.ErrorIs(t, err, wantErr)
				assert.Equal(t, 1, observed.retries)
				after, exists := store.Get(toolsetCatalogKey("tools"))
				assert.True(t, exists)
				assert.Equal(t, before, after)
				assert.Equal(t, 1, store.definitionWrites)
				assert.Empty(t, store.retiredTokens)
			})
		}
	}
}
