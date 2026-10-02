package runtime

// context_lock.go lets Seal stop waiting for registration or another Seal when
// its context ends. Registrations keep ownership until their callbacks and
// registry updates finish. The zero value is ready to use.

import (
	"context"
	"sync"
)

type contextLock struct {
	mu        sync.Mutex
	ownerDone chan struct{}
}

func (l *contextLock) Lock() {
	for {
		acquired, ownerDone := l.tryLock()
		if acquired {
			return
		}
		<-ownerDone
	}
}

func (l *contextLock) LockContext(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		acquired, ownerDone := l.tryLock()
		if acquired {
			return nil
		}
		select {
		case <-ownerDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (l *contextLock) Unlock() {
	l.mu.Lock()
	defer l.mu.Unlock()
	close(l.ownerDone)
	l.ownerDone = nil
}

// tryLock gives one caller ownership or the current owner's completion channel.
// The caller waits on that channel without holding the internal mutex.
func (l *contextLock) tryLock() (bool, <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ownerDone == nil {
		l.ownerDone = make(chan struct{})
		return true, nil
	}
	return false, l.ownerDone
}
