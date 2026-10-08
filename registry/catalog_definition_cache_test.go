// These tests verify that validation reuse follows exact saved bytes and rules,
// while cold validation cannot block a lookup whose definition is already warm.
package registry

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	// validationWaitContext reports when validation begins waiting for capacity,
	// so the test cancels an already waiting read rather than racing its start.
	validationWaitContext struct {
		context.Context
		waiting chan struct{}
	}
)

func TestCatalogDefinitionCacheRequiresExactBytesAndValidationMode(t *testing.T) {
	t.Parallel()
	catalog, store, _ := testDefinitionCatalog(t)
	first, err := catalog.ActiveRegistration(t.Context(), "tools")
	require.NoError(t, err)
	key := toolsetCatalogKey("tools")
	original := first.Toolset.raw
	store.mu.Lock()
	store.definitions[key] = " \n" + original
	store.mu.Unlock()
	changed, err := catalog.ActiveRegistration(t.Context(), "tools")
	require.NoError(t, err)
	assert.NotSame(t, first.Toolset.catalogDefinition, changed.Toolset.catalogDefinition)
	assert.Equal(t, first.SchemaFingerprint, changed.SchemaFingerprint)
	assert.Equal(t, " \n"+original, changed.Toolset.raw)
	assert.Len(t, catalog.definitions, 1)

	// The same bytes accepted for a service must pass native-agent rules when
	// current persisted state changes the execution kind.
	state := changed.catalogState
	state.NativeAgent = true
	state.RegistrationToken = nativeAgentToken(state.SchemaFingerprint)
	state.AdmissionRevision = ""
	state.WireProtocolVersion = 0
	state.ProviderLeases = nil
	state.HealthEpoch = 0
	state.LastPongUnixNano = 0
	body, err := marshalCatalogState(state)
	require.NoError(t, err)
	store.mu.Lock()
	store.content[key] = body
	store.mu.Unlock()
	_, err = catalog.ActiveRegistration(t.Context(), "tools")
	assert.ErrorContains(t, err, "not a native Agent tool")
}

func TestCatalogWarmReadDoesNotWaitForColdDefinitionValidation(t *testing.T) {
	t.Parallel()
	catalog, _, _ := testDefinitionCatalog(t)
	first, err := catalog.ActiveRegistration(t.Context(), "tools")
	require.NoError(t, err)
	catalog.definitionLoads <- struct{}{}
	defer func() { <-catalog.definitionLoads }()
	finished := make(chan catalogEntry, 1)
	failures := make(chan error, 1)
	go func() {
		entry, readErr := catalog.ActiveRegistration(t.Context(), "tools")
		if readErr != nil {
			failures <- readErr
			return
		}
		finished <- entry
	}()
	select {
	case entry := <-finished:
		assert.Same(t, first.Toolset.catalogDefinition, entry.Toolset.catalogDefinition)
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("warm lookup waited for cold definition validation")
	}
}

func TestCatalogDefinitionDigestIncludesEveryByte(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "small", strings.Repeat("saved bytes", 10_000)} {
		assert.Equal(t, sha256.Sum256([]byte(raw)), catalogDefinitionDigest(raw))
	}
}

func TestCatalogColdReadCancellationReleasesWaitingDefinition(t *testing.T) {
	t.Parallel()
	writer, store, clock := testDefinitionCatalog(t)
	entry, err := writer.ActiveRegistration(t.Context(), "tools")
	require.NoError(t, err)
	catalog := newToolsetCatalog(store, clock)
	catalog.definitionLoads <- struct{}{}
	defer func() { <-catalog.definitionLoads }()
	cancelable, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &validationWaitContext{Context: cancelable, waiting: make(chan struct{})}
	finished := make(chan error, 1)
	go func() {
		_, validationErr := catalog.validatedDefinition(ctx, "tools", entry.Toolset.raw, false)
		finished <- validationErr
	}()
	select {
	case <-ctx.waiting:
	case <-time.After(time.Second):
		t.Fatal("cold lookup did not reach the validation queue")
	}
	cancel()
	select {
	case err := <-finished:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("canceled lookup retained its definition while waiting")
	}
	assert.Empty(t, catalog.definitions)
	assert.Empty(t, catalog.validator.compiled)
}

func (c *validationWaitContext) Done() <-chan struct{} {
	close(c.waiting)
	return c.Context.Done()
}
