// These tests use saved generated records and independent HTTPS issuers to prove
// restart reuse, serialized rotation and save-before-dispatch. File storage is
// only a test backend; a deployed host owns its encrypted durable implementation.
package mcp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genaccesstokens "goa.design/goa-ai/internal/mcpauth/gen/access_tokens"
)

type (
	// fileAuthorizationStore models two host instances sharing one account's files
	// and storage-owned exclusive access. No credential is retained in its fields.
	fileAuthorizationStore struct {
		directory string
		lock      chan struct{}
	}
	fileAuthorizationCredential struct {
		ctx      context.Context
		filename string
	}
	// failingAuthorizationStore models both certain and uncertain save failures.
	failingAuthorizationStore struct {
		inner  AuthorizationStore
		failAt int
		saves  int
		commit bool
	}
	failingAuthorizationCredential struct {
		inner AuthorizationCredential
		store *failingAuthorizationStore
	}
)

func TestAuthorizationStoreRestartReuseAndRotation(t *testing.T) {
	peer := newBrowserOAuthPeer(t)
	peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"private-refresh-one"}`
	directory := t.TempDir()
	lock := make(chan struct{}, 1)
	first := peer.transportWithStore(t, peer.authorize(t), &fileAuthorizationStore{directory: directory, lock: lock})
	require.NoError(t, callOAuthPeer(t.Context(), first, peer.resource))
	issuance := storedOAuthCredential(t, first).Issuance
	restarted := peer.transportWithStore(t, peer.authorize(t), &fileAuthorizationStore{directory: directory, lock: lock})
	require.NoError(t, callOAuthPeer(t.Context(), restarted, peer.resource))
	assert.Equal(t, issuance, storedOAuthCredential(t, restarted).Issuance)
	assert.EqualValues(t, 1, peer.hostCalls.Load())
	assert.EqualValues(t, 1, peer.tokenCalls.Load())
	setOAuthCredentialTime(t, restarted, time.Now().Add(-2*time.Hour))
	peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"private-refresh-two"}`
	require.NoError(t, callOAuthPeer(t.Context(), restarted, peer.resource))
	again := peer.transportWithStore(t, peer.authorize(t), &fileAuthorizationStore{directory: directory, lock: lock})
	require.NoError(t, callOAuthPeer(t.Context(), again, peer.resource))
	assert.NotEqual(t, issuance, storedOAuthCredential(t, again).Issuance)
	assert.Equal(t, "private-refresh-two", *storedOAuthCredential(t, again).Token.RefreshToken)
	assert.EqualValues(t, 1, peer.hostCalls.Load())
	assert.EqualValues(t, 2, peer.tokenCalls.Load())
	peer.mutex.Lock()
	defer peer.mutex.Unlock()
	require.Len(t, peer.forms, 2)
	assert.Equal(t, "refresh_token", peer.forms[1].Get("grant_type"))
	assert.Equal(t, "private-refresh-one", peer.forms[1].Get("refresh_token"))
}

func TestAuthorizationStoreConcurrentStaleRejections(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	peer := newBrowserOAuthPeer(t)
	peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"private-refresh"}`
	directory := t.TempDir()
	lock := make(chan struct{}, 1)
	transports := make([]*HTTPTransport, 5)
	for n := range transports {
		transports[n] = peer.transportWithStore(t, peer.authorize(t), &fileAuthorizationStore{directory: directory, lock: lock})
	}
	require.NoError(t, callOAuthPeer(t.Context(), transports[0], peer.resource))
	peer.tokenReply = func(int32) string { return `{"access_token":"changed-token","token_type":"Bearer","expires_in":3600}` }
	var stale atomic.Int64
	allSent := make(chan struct{})
	peer.mcpHandle = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") == "Bearer changed-token" {
			return false
		}
		if stale.Add(1) == int64(len(transports)) {
			close(allSent)
		}
		select {
		case <-allSent:
		case <-r.Context().Done():
			return true
		}
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		w.WriteHeader(http.StatusUnauthorized)
		return true
	}
	var calls sync.WaitGroup
	for _, transport := range transports {
		calls.Go(func() { assert.NoError(t, callOAuthPeer(ctx, transport, peer.resource)) })
	}
	calls.Wait()
	assert.EqualValues(t, 2, peer.tokenCalls.Load())
	assert.EqualValues(t, 1, peer.hostCalls.Load())
}

func TestAuthorizationStoreSaveFailureStopsDispatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		save   int
		commit bool
		host   int32
	}{
		{"pending not committed", 1, false, 1},
		{"pending committed with lost acknowledgement", 1, true, 1},
		{"ready not committed", 2, false, 2},
		{"ready committed with lost acknowledgement", 2, true, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			peer := newBrowserOAuthPeer(t)
			peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"private-refresh"}`
			inner := NewMemoryAuthorizationStore()
			store := &failingAuthorizationStore{inner: inner, failAt: test.save, commit: test.commit}
			transport := peer.transportWithStore(t, peer.authorize(t), store)
			err := callOAuthPeer(t.Context(), transport, peer.resource)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private-storage-canary")
			assert.Zero(t, peer.mcpCalls.Load())
			if test.save == 1 {
				assert.Zero(t, peer.tokenCalls.Load())
				assert.Zero(t, peer.hostCalls.Load())
			}
			recovered := peer.transportWithStore(t, peer.authorize(t), inner)
			require.NoError(t, callOAuthPeer(t.Context(), recovered, peer.resource))
			assert.Equal(t, test.host, peer.hostCalls.Load())
			assert.Equal(t, test.host, peer.tokenCalls.Load())
			if test.host == 2 {
				peer.mutex.Lock()
				defer peer.mutex.Unlock()
				require.Len(t, peer.forms, 2)
				assert.NotEqual(t, peer.forms[0].Get("code"), peer.forms[1].Get("code"))
				assert.Empty(t, peer.forms[1].Get("refresh_token"))
			}
		})
	}
}

func TestAuthorizationStoreUncertainRefreshUsesFreshConsent(t *testing.T) {
	peer := newBrowserOAuthPeer(t)
	peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600,"refresh_token":"private-refresh"}`
	store := NewMemoryAuthorizationStore()
	first := peer.transportWithStore(t, peer.authorize(t), store)
	require.NoError(t, callOAuthPeer(t.Context(), first, peer.resource))
	setOAuthCredentialTime(t, first, time.Now().Add(-2*time.Hour))
	peer.tokenBody = `{"access_token":"private-malformed-response","token_type":"wrong"}`
	require.Error(t, callOAuthPeer(t.Context(), first, peer.resource))
	assert.EqualValues(t, 1, peer.mcpCalls.Load())
	peer.tokenBody = `{"access_token":"opaque-token","token_type":"Bearer","expires_in":3600}`
	restarted := peer.transportWithStore(t, peer.authorize(t), store)
	require.NoError(t, callOAuthPeer(t.Context(), restarted, peer.resource))
	peer.mutex.Lock()
	defer peer.mutex.Unlock()
	require.Len(t, peer.forms, 3)
	assert.Equal(t, "refresh_token", peer.forms[1].Get("grant_type"))
	assert.Equal(t, "authorization_code", peer.forms[2].Get("grant_type"))
	assert.Empty(t, peer.forms[2].Get("refresh_token"))
	assert.EqualValues(t, 2, peer.hostCalls.Load())
}

func TestAuthorizationStoreRejectsCorruptAndMisboundRecords(t *testing.T) {
	for _, test := range []struct {
		name    string
		corrupt func([]byte) []byte
	}{
		{"invalid JSON", func([]byte) []byte { return []byte(`{"private-canary":`) }},
		{"unknown property", func(data []byte) []byte {
			return bytes.Replace(data, []byte("{"), []byte(`{"private-canary":true,`), 1)
		}},
		{"another owner", func(data []byte) []byte {
			value, err := genaccesstokens.DecodeResourceCredentialState(data)
			require.NoError(t, err)
			value.Binding[len(value.Binding)-1] = "https://another.example/mcp"
			data, err = genaccesstokens.EncodeResourceCredentialState(value)
			require.NoError(t, err)
			return data
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			peer := newBrowserOAuthPeer(t)
			store := NewMemoryAuthorizationStore()
			first := peer.transportWithStore(t, peer.authorize(t), store)
			require.NoError(t, callOAuthPeer(t.Context(), first, peer.resource))
			require.NoError(t, withTestAuthorizationCredential(t.Context(), store, first.authorization.grant.credentialBindings(first.authorization.resource.String())[0], func(record AuthorizationCredential) error {
				data, exists, err := record.Load()
				require.NoError(t, err)
				require.True(t, exists)
				return record.Save(test.corrupt(data))
			}))
			next := peer.transportWithStore(t, peer.authorize(t), store)
			err := callOAuthPeer(t.Context(), next, peer.resource)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private-canary")
			assert.EqualValues(t, 1, peer.hostCalls.Load())
			assert.EqualValues(t, 1, peer.tokenCalls.Load())
			assert.EqualValues(t, 1, peer.mcpCalls.Load())
		})
	}
}

func TestAuthorizationStoreSAMLRestartAndFreshRecovery(t *testing.T) {
	peer := newEnterpriseOAuthPeer(t, oauthPublicClient, oauthPublicClient)
	directory := t.TempDir()
	lock := make(chan struct{}, 1)
	store := &fileAuthorizationStore{directory: directory, lock: lock}
	first := peer.transport(t, peer.identityWithStore(t, "SAML", store))
	require.NoError(t, callOAuthPeer(t.Context(), first, peer.resource.resource))
	setOAuthCredentialTime(t, first, time.Now().Add(-2*time.Hour))
	restarted := peer.transport(t, peer.identityWithStore(t, "SAML", &fileAuthorizationStore{directory: directory, lock: lock}))
	require.NoError(t, callOAuthPeer(t.Context(), restarted, peer.resource.resource))
	assert.EqualValues(t, 1, peer.sourceCalls.Load())
	assert.Len(t, peer.forms, 3)
	// A different host user's store cannot load either credential from this user.
	separate := peer.transport(t, peer.identityWithStore(t, "SAML", NewMemoryAuthorizationStore()))
	require.NoError(t, callOAuthPeer(t.Context(), separate, peer.resource.resource))
	assert.EqualValues(t, 2, peer.sourceCalls.Load())
}

func TestAuthorizationStoreEnterpriseSaveFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		save   int
		commit bool
		source int32
		tokens int32
	}{
		{"identity pending failure", 2, false, 1, 1},
		{"identity ready not committed", 3, false, 2, 1},
		{"identity ready committed with lost acknowledgement", 3, true, 1, 1},
		{"resource ready not committed", 4, false, 1, 2},
		{"resource ready committed with lost acknowledgement", 4, true, 1, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			peer := newEnterpriseOAuthPeer(t, oauthPublicClient, oauthPublicClient)
			inner := NewMemoryAuthorizationStore()
			store := &failingAuthorizationStore{inner: inner, failAt: test.save, commit: test.commit}
			first := peer.transport(t, peer.identityWithStore(t, "SAML", store))
			err := callOAuthPeer(t.Context(), first, peer.resource.resource)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private-storage-canary")
			assert.Zero(t, peer.resource.mcpCalls.Load())
			if test.save == 2 {
				assert.Zero(t, peer.sourceCalls.Load())
			}
			next := peer.transport(t, peer.identityWithStore(t, "SAML", inner))
			require.NoError(t, callOAuthPeer(t.Context(), next, peer.resource.resource))
			assert.Equal(t, test.source, peer.sourceCalls.Load())
			assert.Equal(t, test.tokens, peer.resource.tokenCalls.Load())
			if test.source == 2 {
				peer.mutex.Lock()
				defer peer.mutex.Unlock()
				require.GreaterOrEqual(t, len(peer.forms), 2)
				assert.NotEqual(t, peer.forms[0].Get("subject_token"), peer.forms[1].Get("subject_token"))
			}
		})
	}
}

func TestAuthorizationStoreWaitCancellationAndIndependentKeys(t *testing.T) {
	store := NewMemoryAuthorizationStore()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	t.Cleanup(func() {
		close(release)
		require.NoError(t, <-finished)
	})
	go func() {
		finished <- store.WithCredentials(t.Context(), []string{"first"}, func([]AuthorizationCredential) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := store.WithCredentials(ctx, []string{"first"}, func([]AuthorizationCredential) error {
		t.Error("canceled waiter invoked callback")
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, store.WithCredentials(t.Context(), []string{"second"}, func(records []AuthorizationCredential) error {
		return records[0].Save([]byte("independent"))
	}))
}

// storedOAuthCredential reads the same persisted representation as a restarted
// client, so assertions never rely on the previous process's cached pointers.
func storedOAuthCredential(t *testing.T, transport *HTTPTransport) *genaccesstokens.ResourceCredentialReady {
	t.Helper()
	var ready *genaccesstokens.ResourceCredentialReady
	require.NoError(t, withTestAuthorizationCredential(t.Context(), transport.authorization.store, transport.authorization.grant.credentialBindings(transport.authorization.resource.String())[0], func(record AuthorizationCredential) error {
		data, exists, err := record.Load()
		require.NoError(t, err)
		require.True(t, exists)
		state, err := genaccesstokens.DecodeResourceCredentialState(data)
		require.NoError(t, err)
		var available bool
		ready, available = state.State.AsReady()
		require.True(t, available)
		return nil
	}))
	return ready
}

// setOAuthCredentialTime supplies an expired saved token without waiting for an
// issuer lifetime to elapse. Subsequent clients must reload this saved value.
func setOAuthCredentialTime(t *testing.T, transport *HTTPTransport, obtained time.Time) {
	t.Helper()
	require.NoError(t, withTestAuthorizationCredential(t.Context(), transport.authorization.store, transport.authorization.grant.credentialBindings(transport.authorization.resource.String())[0], func(record AuthorizationCredential) error {
		data, exists, err := record.Load()
		require.NoError(t, err)
		require.True(t, exists)
		state, err := genaccesstokens.DecodeResourceCredentialState(data)
		require.NoError(t, err)
		ready, available := state.State.AsReady()
		require.True(t, available)
		ready.Obtained = obtained.Format(time.RFC3339Nano)
		state.State = genaccesstokens.NewStateReady(ready)
		data, err = genaccesstokens.EncodeResourceCredentialState(state)
		require.NoError(t, err)
		return record.Save(data)
	}))
}

func (s *fileAuthorizationStore) WithCredentials(ctx context.Context, keys []string, use func([]AuthorizationCredential) error) error {
	select {
	case s.lock <- struct{}{}:
		defer func() { <-s.lock }()
	case <-ctx.Done():
		return ctx.Err()
	}
	records := make([]AuthorizationCredential, len(keys))
	for n, key := range keys {
		records[n] = &fileAuthorizationCredential{ctx: ctx, filename: filepath.Join(s.directory, key)}
	}
	return use(records)
}

func (r *fileAuthorizationCredential) Load() ([]byte, bool, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(r.filename)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func (r *fileAuthorizationCredential) Save(data []byte) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	return os.WriteFile(r.filename, data, 0o600)
}

func (s *failingAuthorizationStore) WithCredentials(ctx context.Context, keys []string, use func([]AuthorizationCredential) error) error {
	return s.inner.WithCredentials(ctx, keys, func(records []AuthorizationCredential) error {
		wrapped := make([]AuthorizationCredential, len(records))
		for n, record := range records {
			wrapped[n] = &failingAuthorizationCredential{inner: record, store: s}
		}
		return use(wrapped)
	})
}

func (r *failingAuthorizationCredential) Load() ([]byte, bool, error) {
	return r.inner.Load()
}

func (r *failingAuthorizationCredential) Save(data []byte) error {
	r.store.saves++
	fail := r.store.saves == r.store.failAt
	if !fail || r.store.commit {
		if err := r.inner.Save(data); err != nil {
			return err
		}
	}
	if fail {
		return errors.New("private-storage-canary")
	}
	return nil
}

// withTestAuthorizationCredential inspects one generated saved record without
// acquiring the other records needed only while issuing a complete grant.
func withTestAuthorizationCredential(ctx context.Context, store AuthorizationStore, binding []string, use func(AuthorizationCredential) error) error {
	return withAuthorizationCredentials(ctx, store, []string{authorizationCredentialKey(binding)}, func(_ context.Context, records []AuthorizationCredential) error {
		return use(records[0])
	})
}
