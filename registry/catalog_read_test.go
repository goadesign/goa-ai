package registry

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestCatalogIdentityPreservesDeclarationAndAdmission(t *testing.T) {
	ctx := context.Background()
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	catalog := newToolsetCatalog(store, clock)
	input := testCatalogToolset("route", "tool", []string{"retained"})
	definition := testCatalogDefinition(t, input)
	original := string(definition.raw)
	definition.identity = &CatalogIdentity{Scope: "product", Name: "Public name / unchanged"}
	first, err := catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	require.Equal(t, original, store.definitions[toolsetCatalogKey("route")])
	member, err := store.Contains(ctx, "product", "route")
	require.NoError(t, err)
	require.True(t, member)
	repeated, err := catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	require.Equal(t, first.RegistrationToken, repeated.RegistrationToken)
	require.Equal(t, first.RegisteredAt, repeated.RegisteredAt)

	for _, replacement := range []*CatalogIdentity{nil, {Scope: "other", Name: definition.identity.Name}, {Scope: "product", Name: "other"}} {
		changed := testCatalogDefinition(t, input)
		changed.identity = replacement
		_, err := catalog.Register(ctx, changed, testAdmissionRevisionA, "provider", testIncarnationB, time.Minute)
		require.ErrorIs(t, err, errAdmissionConflict)
	}
	require.Equal(t, original, store.definitions[toolsetCatalogKey("route")])
}

func TestBoundedCatalogReadCountsStateAndPreservesOversizedRecord(t *testing.T) {
	ctx := context.Background()
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	catalog := newToolsetCatalog(store, clock)
	input := &genregistry.Toolset{Name: "route", Tools: testServiceDeclaration().Tools}
	description := strings.Repeat("<", 1024)
	input.Tools[0].Description = &description
	definition := testCatalogDefinition(t, input)
	definition.identity = &CatalogIdentity{Scope: "product", Name: "route"}
	admitted, err := catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	key := toolsetCatalogKey("route")
	raw, stored := store.content[key], store.definitions[key]
	require.Contains(t, stored, strings.Repeat(`\u003c`, 1024))
	total := int64(len(raw) + len(stored))
	_, err = store.BoundedSnapshot(ctx, key, total-1)
	require.ErrorIs(t, err, ErrCatalogReadBudget)
	require.Zero(t, store.snapshotReads)
	at, err := store.BoundedSnapshot(ctx, key, total)
	require.NoError(t, err)
	require.Equal(t, raw, at.state)
	require.Equal(t, stored, at.definition)
	require.Equal(t, raw, store.content[key])
	repeated, err := catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	require.Equal(t, admitted.RegistrationToken, repeated.RegistrationToken)
}

func (m *testCatalogMap) BoundedSnapshot(ctx context.Context, key string, budget int64) (catalogSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return catalogSnapshot{}, err
	}
	state, stateExists := m.content[key]
	definition, definitionExists := m.definitions[key]
	if stateExists != definitionExists {
		return catalogSnapshot{}, errors.New("CATALOGINCOMPLETE")
	}
	if !stateExists {
		return catalogSnapshot{}, nil
	}
	if int64(len(state))+int64(len(definition)) > budget {
		return catalogSnapshot{}, ErrCatalogReadBudget
	}
	var identity struct {
		RegistrationToken string `json:"registration_token"`
	}
	if err := json.Unmarshal([]byte(state), &identity); err != nil {
		return catalogSnapshot{}, err
	}
	now, err := m.clock.Now(ctx)
	if err != nil {
		return catalogSnapshot{}, err
	}
	_, retired := m.retiredTokens[identity.RegistrationToken]
	m.snapshotReads++
	return catalogSnapshot{state: state, definition: definition, exists: true, retired: retired, now: now}, nil
}

func (m *testCatalogMap) After(ctx context.Context, scope, after string, count int) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var routes []string
	for route := range m.scopeRoutes[scope] {
		if route > after {
			routes = append(routes, route)
		}
	}
	slices.Sort(routes)
	return routes[:min(count, len(routes))], nil
}

func (m *testCatalogMap) Contains(ctx context.Context, scope, name string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, exists := m.scopeRoutes[scope][name]
	return exists, nil
}

func (m *testCatalogMap) indexWrite(key string, next catalogWrite) {
	if next.Scope == "" {
		return
	}
	routes := m.scopeRoutes[next.Scope]
	if routes == nil {
		routes = make(map[string]struct{})
		m.scopeRoutes[next.Scope] = routes
	}
	route := strings.TrimPrefix(key, toolsetCatalogKeyPrefix)
	if next.Indexed {
		routes[route] = struct{}{}
	} else {
		delete(routes, route)
	}
}
