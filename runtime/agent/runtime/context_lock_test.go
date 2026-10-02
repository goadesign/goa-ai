package runtime

// These tests hold ownership until the test releases it. A canceled acquisition
// must return before that release, without taking ownership from the holder.

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContextLockWaitEndsBeforeOwnerRelease(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancel"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var lock contextLock
				lock.Lock()
				ctx, cancel := context.WithCancel(context.Background())
				want := context.Canceled
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), time.Minute)
					want = context.DeadlineExceeded
				}
				defer cancel()
				result := make(chan error, 1)
				go func() { result <- lock.LockContext(ctx) }()
				if !deadline {
					synctest.Wait()
					cancel()
				}
				require.ErrorIs(t, <-result, want)
				acquired, _ := lock.tryLock()
				assert.False(t, acquired)
				lock.Unlock()
				require.NoError(t, lock.LockContext(context.Background()))
				lock.Unlock()
			})
		})
	}
}

func TestContextLockBlockingOwnerAndPanicRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var lock contextLock
		lock.Lock()
		next := make(chan struct{})
		go func() {
			lock.Lock()
			defer lock.Unlock()
			close(next)
		}()
		synctest.Wait()
		select {
		case <-next:
			t.Fatal("another caller obtained held ownership")
		default:
		}
		lock.Unlock()
		<-next
		synctest.Wait()

		require.PanicsWithValue(t, "original panic", func() {
			lock.Lock()
			defer lock.Unlock()
			panic("original panic")
		})
		require.NoError(t, lock.LockContext(context.Background()))
		lock.Unlock()
	})
}
